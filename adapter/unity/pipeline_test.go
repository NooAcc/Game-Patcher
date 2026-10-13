package unity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"game-patcher/adapter"
	"game-patcher/patcher"
)

// TestEndToEndRewritePipeline 走完整链路：生成补丁 -> 应用 -> 校验重建后的容器。
// 它验证逻辑差分管线在真实 patcher 编排下能产出与新版逐字节等价的逻辑内容。
func TestEndToEndRewritePipeline(t *testing.T) {
	// 旧内容是一段可压缩的重复数据；新内容在中段插入了一段新数据。
	oldStream := bytes.Repeat([]byte("0123456789abcdef-unity-asset-chunk-"), 8000)
	newStream := append([]byte{}, oldStream[:70000]...)
	newStream = append(newStream, bytes.Repeat([]byte("NEW-RESOURCE-DATA"), 4000)...)
	newStream = append(newStream, oldStream[70000:]...)

	oldNodes := []nodeInfo{
		{offset: 0, size: int64(len(oldStream)), flags: 4, path: "level0"},
	}
	newNodes := []nodeInfo{
		{offset: 0, size: int64(len(newStream)), flags: 4, path: "level0"},
	}
	oldData := buildContainer(t, oldStream, 32768, oldNodes)
	newData := buildContainer(t, newStream, 32768, newNodes)

	work := t.TempDir()
	oldDir := filepath.Join(work, "v1")
	newDir := filepath.Join(work, "v2")
	for _, d := range []string{oldDir, newDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(oldDir, "data.unity3d"), oldData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "data.unity3d"), newData, 0o644); err != nil {
		t.Fatal(err)
	}

	baseExe := filepath.Join(work, "base.exe")
	if err := os.WriteFile(baseExe, []byte("MZ-dummy-base-executable-for-tests"), 0o644); err != nil {
		t.Fatal(err)
	}
	updater := filepath.Join(work, "updater.exe")
	if err := patcher.CreatePatchWithOptions(patcher.CreatePatchOptions{
		BaseExe: baseExe,
		OldDir:  oldDir,
		NewDir:  newDir,
		Output:  updater,
		AdapterOptions: adapter.Options{Values: map[string]string{
			adapter.OptionSelected: "unity",
		}},
	}); err != nil {
		t.Fatalf("CreatePatchWithOptions: %v", err)
	}

	// 在“玩家目录”里放一份旧文件与升级工具，然后应用补丁。
	gameDir := filepath.Join(work, "game")
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(gameDir, "data.unity3d")
	if err := os.WriteFile(target, oldData, 0o644); err != nil {
		t.Fatal(err)
	}
	updaterInGame := filepath.Join(gameDir, "game-updater.exe")
	if err := os.WriteFile(updaterInGame, mustRead(t, updater), 0o644); err != nil {
		t.Fatal(err)
	}

	// RunEmbedded 会从标准输入读取确认；测试环境用空设备避免阻塞。
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = devNull
	defer func() {
		os.Stdin = saved
		devNull.Close()
	}()

	if err := patcher.RunEmbedded(updaterInGame); err != nil {
		t.Fatalf("RunEmbedded: %v", err)
	}

	c, err := openContainer(target)
	if err != nil {
		t.Fatalf("重建结果不是可解析的 UnityFS 容器: %v", err)
	}
	defer c.Close()
	got := readAll(t, c.decoded())
	if !bytes.Equal(got, newStream) {
		t.Fatalf("重建后逻辑内容与新版本不一致（got %d bytes, want %d）", len(got), len(newStream))
	}
	if len(c.nodes) != len(newNodes) || c.nodes[0].path != "level0" {
		t.Fatalf("节点表不一致: %+v", c.nodes)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
