package patcher

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zeebo/blake3"
)

// blobPool 去重存放所有段的字面量块，键为块内容的 BLAKE3。
type blobPool struct {
	mu    sync.Mutex
	blobs []Blob
	index map[[32]byte]uint32
}

func newBlobPool() *blobPool {
	return &blobPool{index: make(map[[32]byte]uint32)}
}

// blobPoolFrom 用已有块池重建去重索引（用于 -prev 的跨次构建去重）。
func blobPoolFrom(blobs []Blob) *blobPool {
	p := &blobPool{blobs: blobs, index: make(map[[32]byte]uint32, len(blobs))}
	for i := range blobs {
		if _, ok := p.index[blobs[i].Hash]; !ok {
			p.index[blobs[i].Hash] = uint32(i)
		}
	}
	return p
}

// intern 把一段原始数据放入池中。相同内容只存一份，返回池下标。
// 压缩在锁外完成，避免并行构建时被串行化。
func (p *blobPool) intern(raw []byte) uint32 {
	key := HashBytes(raw)
	p.mu.Lock()
	if idx, ok := p.index[key]; ok {
		p.mu.Unlock()
		return idx
	}
	p.mu.Unlock()

	comp, payload := compressLiteral(raw)

	p.mu.Lock()
	defer p.mu.Unlock()
	if idx, ok := p.index[key]; ok {
		return idx
	}
	idx := uint32(len(p.blobs))
	p.blobs = append(p.blobs, Blob{Hash: key, Comp: comp, RawLen: uint64(len(raw)), Data: payload})
	p.index[key] = idx
	return idx
}

// crossRef 是跨文件块索引中的一条引用。
type crossRef struct {
	path   string
	offset uint64
	size   int
}

// crossIndex 索引"本段被删除文件"的块，供本段新增/修改文件复用。
// 由于同段内 delete 最后执行，这些源文件在读取阶段始终存在且内容未被修改。
type crossIndex struct {
	m map[[32]byte]crossRef
}

func newCrossIndex() *crossIndex {
	return &crossIndex{m: make(map[[32]byte]crossRef)}
}

func (c *crossIndex) addFile(root, rel string) error {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	defer f.Close()

	sc := newChunkScanner(f)
	var offset uint64
	for {
		ch, err := sc.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		key := blake3.Sum256(ch)
		if _, ok := c.m[key]; !ok {
			c.m[key] = crossRef{path: rel, offset: offset, size: len(ch)}
		}
		offset += uint64(len(ch))
	}
	return nil
}

func (c *crossIndex) lookup(key [32]byte, size int) (crossRef, bool) {
	ref, ok := c.m[key]
	if !ok || ref.size != size {
		return crossRef{}, false
	}
	return ref, true
}

// buildChunkFileEntry 生成一个文件的重建操作（chunk 后端）。
// oldPath 为空表示新增文件（没有本文件旧内容可复制）。
func buildChunkFileEntry(oldPath, newPath, relPath string, cross *crossIndex, pool *blobPool) (*deltaResult, error) {
	var (
		index   map[[32]byte]chunkRef
		oldSize uint64
		oldHash [32]byte
	)
	if oldPath != "" {
		var err error
		index, oldSize, oldHash, err = indexChunks(oldPath)
		if err != nil {
			return nil, fmt.Errorf("扫描旧文件失败: %w", err)
		}
	}

	newFile, err := os.Open(newPath)
	if err != nil {
		return nil, fmt.Errorf("打开新文件失败: %w", err)
	}
	defer newFile.Close()

	hasher := blake3.New()
	sc := newChunkScanner(io.TeeReader(newFile, hasher))

	var ops []DeltaOp
	var literal []byte
	flush := func() {
		if len(literal) == 0 {
			return
		}
		idx := pool.intern(literal)
		ops = append(ops, DeltaOp{Kind: OpPoolRef, PoolIndex: idx, Length: uint64(len(literal))})
		literal = literal[:0]
	}

	var newSize uint64
	for {
		ch, err := sc.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("扫描新文件失败: %w", err)
		}
		newSize += uint64(len(ch))
		sum := blake3.Sum256(ch)

		if ref, ok := index[sum]; ok && ref.size == len(ch) {
			flush()
			appendCopyOp(&ops, ref.offset, uint64(len(ch)))
			continue
		}
		if ref, ok := cross.lookup(sum, len(ch)); ok {
			flush()
			ops = append(ops, DeltaOp{
				Kind:      OpCopyFrom,
				SrcPath:   ref.path,
				OldOffset: ref.offset,
				Length:    uint64(len(ch)),
			})
			continue
		}
		literal = append(literal, ch...)
		if len(literal) >= maxLiteralRun {
			flush()
		}
	}
	flush()

	var newHash [32]byte
	copy(newHash[:], hasher.Sum(nil))
	return &deltaResult{
		OldHash: oldHash,
		NewHash: newHash,
		OldSize: oldSize,
		NewSize: newSize,
		Ops:     ops,
	}, nil
}

// buildChunkStepEntries 为 oldDir→newDir 构建一段差异（chunk 后端）。
func buildChunkStepEntries(oldDir, newDir string, jobs []treeJob, pool *blobPool) ([]Entry, error) {
	// 1) 索引本段被删除的文件，供新增/修改文件跨文件复用。
	cross := newCrossIndex()
	for i := range jobs {
		if jobs[i].action != ActionDelete {
			continue
		}
		if err := cross.addFile(oldDir, jobs[i].path); err != nil {
			return nil, fmt.Errorf("索引待删除文件 %s 失败: %w", jobs[i].path, err)
		}
	}

	// 2) 并行生成每个条目的操作。
	entries := make([]Entry, len(jobs))
	err := parallelFor(len(jobs), runtime.NumCPU(), func(i int) error {
		j := &jobs[i]
		switch j.action {
		case ActionAdd:
			res, err := buildChunkFileEntry("",
				filepath.Join(newDir, filepath.FromSlash(j.path)), j.path, cross, pool)
			if err != nil {
				return fmt.Errorf("处理新增文件 %s 失败: %w", j.path, err)
			}
			entries[i] = Entry{
				Path:    j.path,
				Action:  ActionAdd,
				NewHash: res.NewHash,
				NewSize: res.NewSize,
				Ops:     res.Ops,
			}
		case ActionUpdate:
			res, err := buildChunkFileEntry(
				filepath.Join(oldDir, filepath.FromSlash(j.path)),
				filepath.Join(newDir, filepath.FromSlash(j.path)),
				j.path, cross, pool,
			)
			if err != nil {
				return fmt.Errorf("计算 %s 差异失败: %w", j.path, err)
			}
			entries[i] = Entry{
				Path:    j.path,
				Action:  ActionUpdate,
				OldHash: res.OldHash,
				NewHash: res.NewHash,
				OldSize: res.OldSize,
				NewSize: res.NewSize,
				Ops:     res.Ops,
			}
		case ActionDelete:
			entries[i] = Entry{
				Path:    j.path,
				Action:  ActionDelete,
				OldHash: j.oldMeta.hash,
				OldSize: j.oldMeta.size,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}
