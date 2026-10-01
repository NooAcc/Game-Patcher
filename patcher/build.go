package patcher

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// CreatePatchOptions 描述一次补丁构建。
type CreatePatchOptions struct {
	BaseExe   string   // 升级工具基础程序
	OldDir    string   // 旧版本目录（本次差异的源）
	NewDir    string   // 新版本目录（本次差异的目标）
	Output    string   // 输出升级工具路径
	Restorer  string   // 可选：恢复工具二进制
	PrevPaths []string // 可选：要附加的旧补丁
}

// CreatePatch 生成单段补丁（等价于不带旧补丁前缀）。
func CreatePatch(baseExe, oldPath, newPath, outputPath, restorerPath string) error {
	return CreatePatchWithOptions(CreatePatchOptions{
		BaseExe: baseExe, OldDir: oldPath, NewDir: newPath, Output: outputPath, Restorer: restorerPath,
	})
}

// CreatePatchChain 生成链式补丁，把 prevPaths 指向的旧补丁作为前缀。
func CreatePatchChain(baseExe, oldPath, newPath, outputPath, restorerPath string, prevPaths []string) error {
	return CreatePatchWithOptions(CreatePatchOptions{
		BaseExe: baseExe, OldDir: oldPath, NewDir: newPath, Output: outputPath,
		Restorer: restorerPath, PrevPaths: prevPaths,
	})
}

// CreatePatchWithOptions 构建补丁：把旧补丁作为版本链前缀，追加本次 oldDir→newDir 的差异段。
// 补丁版本号在前缀版本号基础上自动 +1（无前缀时为 1）。
func CreatePatchWithOptions(opts CreatePatchOptions) error {
	start := time.Now()
	oldPath, newPath, outputPath := opts.OldDir, opts.NewDir, opts.Output

	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		return fmt.Errorf("旧版本路径无效: %w", err)
	}
	newInfo, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("新版本路径无效: %w", err)
	}
	if !oldInfo.IsDir() || !newInfo.IsDir() {
		return fmt.Errorf("仅支持目录模式：-old 与 -new 必须都是文件夹")
	}

	// 1. 载入并校验旧补丁前缀，确保版本链可以拼接。
	prev, err := loadPrevRelease(opts.PrevPaths)
	if err != nil {
		return err
	}
	if prev != nil {
		if err := validateJunction(prev, oldPath); err != nil {
			return err
		}
		fmt.Printf("🔗 已附加旧补丁: v%d（含 %d 段差异）\n", prev.PatchVersion, len(prev.Payload.Steps))
	}

	// 2. 计算本次差异段。
	jobs, err := planStep(oldPath, newPath, outputPath)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		fmt.Println("✅ 两个版本完全相同，无需生成升级包。")
		return nil
	}
	selected, err := selectJobs(jobs)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return ErrCancelled
	}
	fmt.Printf("\n🔧 正在为 %d 个文件计算二进制差异...\n", len(selected))

	// 3. 块池在旧补丁基础上继续累积，实现跨次构建去重。
	pool := newBlobPool()
	if prev != nil {
		pool = blobPoolFrom(prev.Payload.Pool)
	}
	entries, err := buildChunkStepEntries(oldPath, newPath, selected, pool)
	if err != nil {
		return err
	}

	// 4. 拼接版本链。
	var (
		labels       []string
		steps        []ChainStep
		patchVersion = uint32(1)
	)
	if prev != nil {
		labels = append(labels, prev.Payload.Labels...)
		steps = append(steps, prev.Payload.Steps...)
		patchVersion = prev.PatchVersion + 1
	} else {
		labels = append(labels, versionLabel(oldPath))
	}
	steps = append(steps, ChainStep{SourceIndex: uint32(len(steps) + 1), Entries: entries})
	labels = append(labels, versionLabel(newPath))

	payload := NewChunkPayload(labels, steps, pool.blobs)
	if err := payload.SelfCheck(); err != nil {
		return err
	}
	rel := &Release{PatchVersion: patchVersion, Payload: payload}

	if err := writePatchExecutable(opts.BaseExe, rel, outputPath, opts.Restorer); err != nil {
		return err
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		return err
	}
	fmt.Printf("✅ 升级工具已生成: %s (%s) [补丁 v%d, %d 段差异, %d 个共享块, 耗时 %v]\n",
		outputPath, FormatSize(fi.Size()), patchVersion, len(steps), len(pool.blobs),
		time.Since(start).Round(time.Millisecond))
	fmt.Println()
	fmt.Println("💡 将此文件放入游戏目标目录，直接运行即可升级。")
	return nil
}

// loadPrevRelease 读取并校验旧补丁。多次传入时必须属于同一条版本链，
// 返回版本号最高的那一个作为前缀。
func loadPrevRelease(paths []string) (*Release, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	rels := make([]*Release, 0, len(paths))
	for _, p := range paths {
		rel, err := readEmbeddedRelease(p)
		if err != nil {
			return nil, fmt.Errorf("读取旧补丁 %s 失败: %w", p, err)
		}
		rels = append(rels, rel)
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].PatchVersion < rels[j].PatchVersion })

	// 版本号必须连续递增，且每份旧补丁是下一份的链前缀。
	for i := 1; i < len(rels); i++ {
		if rels[i].PatchVersion != rels[i-1].PatchVersion+1 {
			return nil, fmt.Errorf("旧补丁版本号不连续: v%d 与 v%d", rels[i-1].PatchVersion, rels[i].PatchVersion)
		}
		if err := stepsPrefix(rels[i-1].Payload.Steps, rels[i].Payload.Steps); err != nil {
			return nil, fmt.Errorf("旧补丁 v%d 与 v%d 不是同一条版本链: %w",
				rels[i-1].PatchVersion, rels[i].PatchVersion, err)
		}
	}
	if len(rels) > 1 {
		fmt.Printf("🔗 已合并 %d 份旧补丁（v%d..v%d）\n", len(rels), rels[0].PatchVersion, rels[len(rels)-1].PatchVersion)
	}
	return rels[len(rels)-1], nil
}

// validateJunction 校验旧补丁的目标状态与本次的 -old 目录一致，
// 防止把版本链接到错误的源版本上。
func validateJunction(prev *Release, oldDir string) error {
	target := prev.Payload.Target()
	if len(target.Files) == 0 {
		return fmt.Errorf("旧补丁不包含任何版本信息")
	}
	reason, err := matchVersion(oldDir, &target)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("旧补丁的目标版本与 -old 目录不匹配（%s）；请确认旧补丁是 -old 对应版本目录所生成的补丁", reason)
	}
	return nil
}

// stepsPrefix 判断 short 是否为 long 的链前缀（逐段逐条比对）。
func stepsPrefix(short, long []ChainStep) error {
	if len(short) > len(long) {
		return fmt.Errorf("较短补丁的段数 %d 大于较长补丁的段数 %d", len(short), len(long))
	}
	for i := range short {
		a, b := &short[i], &long[i]
		if a.SourceIndex != b.SourceIndex {
			return fmt.Errorf("第 %d 段源版本序号不一致", i+1)
		}
		if len(a.Entries) != len(b.Entries) {
			return fmt.Errorf("第 %d 段条目数不一致", i+1)
		}
		for j := range a.Entries {
			ea, eb := &a.Entries[j], &b.Entries[j]
			if ea.Path != eb.Path || ea.Action != eb.Action ||
				ea.OldHash != eb.OldHash || ea.NewHash != eb.NewHash ||
				ea.OldSize != eb.OldSize || ea.NewSize != eb.NewSize {
				return fmt.Errorf("第 %d 段条目 %s 不一致", i+1, ea.Path)
			}
		}
	}
	return nil
}

func versionLabel(dir string) string {
	label := filepath.Base(filepath.Clean(dir))
	if label == "" || label == "." || label == string(filepath.Separator) {
		return "version"
	}
	return label
}
