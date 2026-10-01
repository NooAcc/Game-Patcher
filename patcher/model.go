package patcher

// FileState 描述某个版本下一条相对路径的状态。
// Exists 为 false 时表示该路径在该版本中不存在（用于检测"新增/删除"）。
type FileState struct {
	Path   string
	Exists bool
	Size   uint64
	Hash   [32]byte
}

// VersionRef 是补丁链中一个版本的指纹，供应用端自动检测使用。
// 版本没有名称：链内序号（Index）就是它在补丁中的唯一标识，
// 因为不同版本的游戏目录名差异很大，记录目录名没有意义。
type VersionRef struct {
	Index uint32      // 版本序号，链内从 1 开始
	Files []FileState // 按 Path 升序，覆盖补丁触及的全部路径
}

// PayloadKind 标识补丁制品的差异数据格式。
type PayloadKind uint8

const (
	// PayloadChunk 是补丁唯一的差异数据格式：版本链 + 共享字面量池 + 跨文件块复用。
	//
	// 共享块池与跨文件复用的实现依赖 chunk.SelfCheck 中校验的 Pool 与 OpCopyFrom
	// 约束，因此 payloadKind 保留了它的值空间。
	PayloadChunk PayloadKind = 2
)

// Blob 是共享字面量池中的一个数据块。
type Blob struct {
	Hash   [32]byte // 原始内容的 BLAKE3，用于跨段/跨次构建去重
	Comp   CompMethod
	RawLen uint64
	Data   []byte
}

// Stage 表示一次从一个版本到下一个版本的状态迁移。
type Stage struct {
	SourceIndex uint32
	Entries     []Entry
	Pool        []Blob // 本段可引用的共享字面量池
}

// Release 是一份补丁制品：制品信封 + 版本链差异数据。
type Release struct {
	PatchVersion uint32        // 自动递增的补丁版本号
	Payload      *ChunkPayload // 补丁差异数据
}
