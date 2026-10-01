package patcher

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type fileMeta struct {
	size uint64
	hash [32]byte
}

// treeJob 是待处理的一个文件变更。
type treeJob struct {
	path    string
	action  Action
	oldMeta fileMeta
	newMeta fileMeta
}

// planStep 扫描两个版本目录，生成变更清单（不计算二进制差异）。
func planStep(oldDir, newDir, skipPath string) ([]treeJob, error) {
	fmt.Println("🔍 扫描旧版本...")
	oldFiles, err := scanTree(oldDir, skipPath)
	if err != nil {
		return nil, fmt.Errorf("扫描旧版本失败: %w", err)
	}
	fmt.Println("🔍 扫描新版本...")
	newFiles, err := scanTree(newDir, skipPath)
	if err != nil {
		return nil, fmt.Errorf("扫描新版本失败: %w", err)
	}

	var jobs []treeJob
	for path, nm := range newFiles {
		if om, ok := oldFiles[path]; ok {
			if om.hash == nm.hash {
				continue
			}
			jobs = append(jobs, treeJob{path: path, action: ActionUpdate, oldMeta: om, newMeta: nm})
			continue
		}
		jobs = append(jobs, treeJob{path: path, action: ActionAdd, newMeta: nm})
	}
	for path, om := range oldFiles {
		if _, ok := newFiles[path]; !ok {
			jobs = append(jobs, treeJob{path: path, action: ActionDelete, oldMeta: om})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].path < jobs[j].path })

	var adds, updates, dels int
	for i := range jobs {
		switch jobs[i].action {
		case ActionAdd:
			adds++
		case ActionUpdate:
			updates++
		case ActionDelete:
			dels++
		}
	}
	fmt.Printf("\n📊 差异: +%d 新增, ~%d 修改, -%d 删除\n", adds, updates, dels)
	return jobs, nil
}

func scanTree(root, skipPath string) (map[string]fileMeta, error) {
	var rels, abs []string
	skipAbs, _ := filepath.Abs(skipPath)

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == backupDirName {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("不支持非普通文件（符号链接/设备等）: %s", p)
		}
		if skipAbs != "" {
			if a, err := filepath.Abs(p); err == nil && strings.EqualFold(a, skipAbs) {
				return nil
			}
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		abs = append(abs, p)
		return nil
	})
	if err != nil {
		return nil, err
	}

	metas := make([]fileMeta, len(abs))
	err = parallelFor(len(abs), runtime.NumCPU(), func(i int) error {
		h, err := HashFile(abs[i])
		if err != nil {
			return fmt.Errorf("哈希 %s 失败: %w", rels[i], err)
		}
		info, err := os.Stat(abs[i])
		if err != nil {
			return err
		}
		metas[i] = fileMeta{size: uint64(info.Size()), hash: h}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make(map[string]fileMeta, len(rels))
	for i := range rels {
		out[rels[i]] = metas[i]
	}
	return out, nil
}

func parallelFor(n, workers int, fn func(i int) error) error {
	if n <= 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	var (
		next     int
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if firstErr != nil || next >= n {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()

				if err := fn(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}
