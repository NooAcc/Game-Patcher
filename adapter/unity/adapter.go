package unity

import (
	"io"

	"game-patcher/adapter"
)

// Adapter 是 Unity3D（UnityFS / .unity3d）引擎适配器。
//
// UnityFS 把内容切成固定大小的 LZ4 压缩块，块边界与文件布局强耦合：某个节点
// 尺寸变化会让后续所有压缩块整体改变。直接做字节差分几乎无法复用旧数据，因此
// 本适配器把差分搬到“解压后的逻辑流”上，并在应用端重新编码容器。
type Adapter struct{}

// New 返回可注册到 adapter.Registry 的 unity 适配器。
func New() adapter.Adapter { return &Adapter{} }

func init() { adapter.Register(New()) }

func (a *Adapter) Name() string { return "unity" }

func (a *Adapter) Description() string {
	return "Unity3D 资源包 UnityFS（块级解码+重编码，推荐）"
}

// Match 只读取两个文件的头部，判断是否都是受支持的 UnityFS 容器。
func (a *Adapter) Match(ctx adapter.Context) adapter.MatchResult {
	if ctx.OldPath == "" || ctx.NewPath == "" {
		return adapter.MatchResult{}
	}
	oldOK, err := probe(ctx.OldPath)
	if err != nil || !oldOK {
		return adapter.MatchResult{}
	}
	newOK, err := probe(ctx.NewPath)
	if err != nil || !newOK {
		return adapter.MatchResult{}
	}
	return adapter.MatchResult{Matched: true, Confidence: 90, Reason: "UnityFS"}
}

// Build 完整解析两个容器；成功时要求 patcher 走逻辑内容重写管线。
func (a *Adapter) Build(ctx adapter.BuildContext) (adapter.BuildResult, error) {
	oldC, err := openContainer(ctx.OldPath)
	if err != nil {
		return adapter.BuildResult{Accepted: false, Reason: "旧文件不是可用的 UnityFS 容器: " + err.Error()}, nil
	}
	defer oldC.Close()

	newC, err := openContainer(ctx.NewPath)
	if err != nil {
		return adapter.BuildResult{Accepted: false, Reason: "新文件不是可用的 UnityFS 容器: " + err.Error()}, nil
	}
	defer newC.Close()

	if len(newC.nodes) == 0 {
		return adapter.BuildResult{Accepted: false, Reason: "新容器没有文件节点表"}, nil
	}
	if newC.streamSize() <= 0 {
		return adapter.BuildResult{Accepted: false, Reason: "新容器逻辑流为空"}, nil
	}
	return adapter.BuildResult{
		Accepted: true,
		Rewrite:  &adapter.RewriteSpec{Meta: encodeMeta(newC.template())},
	}, nil
}

// Decode 返回解压后的逻辑流视图（按需解压，带小块缓存）。
func (a *Adapter) Decode(path string) (adapter.DecodedFile, error) {
	c, err := openContainer(path)
	if err != nil {
		return nil, err
	}
	return c.decoded(), nil
}

// Encode 依据 meta 把逻辑流重新编码成 UnityFS 容器。
func (a *Adapter) Encode(dst io.WriteSeeker, decoded io.Reader, meta []byte) error {
	m, err := decodeMeta(meta)
	if err != nil {
		return err
	}
	return encodeContainer(dst, decoded, m)
}
