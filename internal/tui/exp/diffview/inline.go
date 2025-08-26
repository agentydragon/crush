package diffview

// InlinePiece mirrors diff.Piece for minimal coupling and is exported so
// tests and external providers can construct them.
type InlinePieceKind int

const (
	InlinePieceEqual InlinePieceKind = iota + 1
	InlinePieceInsert
	InlinePieceDelete
)

type InlinePiece struct {
	Kind InlinePieceKind
	Text string
}
