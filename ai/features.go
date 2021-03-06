package ai

import (
	"fmt"
	"log"

	. "github.com/janpfeifer/hiveGo/state"
)

// Enum of feature.
type FeatureId int

// FeatureSetter is the signature of a feature setter. f is the slice where to store the
// results.
// fId is the id of the
type FeatureSetter func(b *Board, def *FeatureDef, f []float32)

const (
	// How many pieces are offboard, per piece type.
	F_NUM_OFFBOARD FeatureId = iota
	F_OPP_NUM_OFFBOARD

	// How many pieces are around the queen (0 if queen hasn't been placed)
	F_NUM_SURROUNDING_QUEEN
	F_OPP_NUM_SURROUNDING_QUEEN

	// How many pieces can move. Two numbers per insect: the first is considering any pieces,
	// the second discards the pieces that are surrounding the opponent's queen (and presumably
	// not to be moved)
	F_NUM_CAN_MOVE
	F_OPP_NUM_CAN_MOVE

	// Number of moves threatening to reach around opponents queen.
	// Two counts here: the first is the number of pieces that can
	// reach around the opponent's queen. The second is the number
	// of free positions around the opponent's queen that can be
	// reached.
	F_NUM_THREATENING_MOVES
	F_OPP_NUM_THREATENING_MOVES

	// Number of moves till a draw due to running out of moves.
	F_MOVES_TO_DRAW

	// Number of pieces that are "leaves" (only one neighbor)
	// First number is for current player, the second is for the
	// opponent.
	F_NUM_SINGLE

	// Whether there is an opponent BEETLE on top of QUEEN.
	F_QUEEN_COVERED

	// Last entry.
	F_NUM_FEATURES
)

// FeatureDef includes feature name, dimension and index in the concatenation of features.
type FeatureDef struct {
	FId  FeatureId
	Name string
	Dim  int

	// VecIndex refers to the index in the concatenated feature vector.
	VecIndex int
	Setter   FeatureSetter

	// Number of feature (AllFeaturesDim) when this feature was created.
	Version int
}

var (
	// Enumeration, in order, of the features extracted by FeatureVector.
	// The VecIndex attribute is properly set during the package initialization.
	// The  "Opp" prefix refers to opponent.
	AllFeatures = [F_NUM_FEATURES]FeatureDef{
		{F_NUM_OFFBOARD, "NumOffboard", int(NUM_PIECE_TYPES), 0, fNumOffBoard, 0},
		{F_OPP_NUM_OFFBOARD, "OppNumOffboard", int(NUM_PIECE_TYPES), 0, fNumOffBoard, 0},

		{F_NUM_SURROUNDING_QUEEN, "NumSurroundingQueen", 1, 0, fNumSurroundingQueen, 0},
		{F_OPP_NUM_SURROUNDING_QUEEN, "OppNumSurroundingQueen", 1, 0, fNumSurroundingQueen, 0},

		{F_NUM_CAN_MOVE, "NumCanMove", 2 * int(NUM_PIECE_TYPES), 0, fNumCanMove, 0},
		{F_OPP_NUM_CAN_MOVE, "OppNumCanMove", 2 * int(NUM_PIECE_TYPES), 0, fNumCanMove, 0},

		{F_NUM_THREATENING_MOVES, "NumThreateningMoves", 2, 0, fNumThreateningMoves, 0},
		{F_OPP_NUM_THREATENING_MOVES, "OppNumThreateningMoves", 2, 0, fNumThreateningMoves, 39},

		{F_MOVES_TO_DRAW, "MovesToDraw", 1, 0, fNumToDraw, 0},
		{F_NUM_SINGLE, "NumSingle", 2, 0, fNumSingle, 0},
		{F_QUEEN_COVERED, "QueenIsCovered", 2, 0, fQueenIsCovered, 41},
	}

	// AllFeaturesDim is the dimension of all features concatenated, set during package
	// initialization.
	AllFeaturesDim int
)

func init() {
	// Updates the indices of AllFeatures, and sets AllFeaturesDim.
	AllFeaturesDim = 0
	for ii := range AllFeatures {
		if AllFeatures[ii].FId != FeatureId(ii) {
			log.Fatalf("ai.AllFeatures index %d for %s doesn't match constant.",
				ii, AllFeatures[ii].Name)
		}
		AllFeatures[ii].VecIndex = AllFeaturesDim
		AllFeaturesDim += AllFeatures[ii].Dim
	}
}

// LabeledExample can be used for training.
type LabeledExample struct {
	Features []float32
	Label    float32

	ActionsFeatures [][]float32 // Optional
	ActionLabels    [][]float32
}

func MakeLabeledExample(board *Board, label float32, version int) LabeledExample {
	return LabeledExample{
		FeatureVector(board, version), label, nil, nil}
}

// FeatureVector calculates the feature vector, of length AllFeaturesDim, for the given
// board.
// Models created at different times may use different subsets of features. This is
// specified by providing the number of features expected by the model.
func FeatureVector(b *Board, version int) (f []float32) {
	if version > AllFeaturesDim {
		log.Panicf("Requested %d features, but only know about %d", version, AllFeaturesDim)
	}
	f = make([]float32, AllFeaturesDim)
	for ii := range AllFeatures {
		featDef := &AllFeatures[ii]
		if featDef.Version <= version {
			featDef.Setter(b, featDef, f)
		}
	}

	if version != AllFeaturesDim {
		// Filter only features for given version.
		newF := make([]float32, 0, version)
		for ii := range AllFeatures {
			featDef := &AllFeatures[ii]
			if featDef.Version <= version {
				newF = append(newF, f[featDef.VecIndex:featDef.VecIndex+featDef.Dim]...)
			}
		}
		f = newF
	}

	return
}

func PrettyPrintFeatures(f []float32) {
	for ii := range AllFeatures {
		def := &AllFeatures[ii]
		fmt.Printf("\t%s: ", def.Name)
		if def.Dim == 1 {
			fmt.Printf("%.2f", f[def.VecIndex])
		} else {
			fmt.Printf("%v", f[def.VecIndex:def.VecIndex+def.Dim])
		}
		fmt.Println()
	}
}

func fNumOffBoard(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	player := b.NextPlayer
	if def.FId == F_OPP_NUM_OFFBOARD {
		player = b.OpponentPlayer()
	}
	for _, piece := range Pieces {
		f[idx+int(piece)-1] = float32(b.Available(player, piece))
	}
}

func fNumSurroundingQueen(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	player := b.NextPlayer
	if def.FId == F_OPP_NUM_SURROUNDING_QUEEN {
		player = b.OpponentPlayer()
	}
	f[idx] = float32(b.Derived.NumSurroundingQueen[player])
}

func fNumCanMove(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	player := b.NextPlayer
	opponent := b.OpponentPlayer()
	if def.FId == F_OPP_NUM_CAN_MOVE {
		player, opponent = opponent, player
	}
	actions := b.Derived.PlayersActions[player]
	var queenNeighbours []Pos
	if b.Available(opponent, QUEEN) == 0 {
		queenNeighbours = b.OccupiedNeighbours(b.Derived.QueenPos[opponent])
	}

	counts := make(map[Piece]int)
	countsNotQueenNeighbours := make(map[Piece]int)
	posVisited := make(map[Pos]bool)
	for _, action := range actions {
		if action.Move && !posVisited[action.SourcePos] {
			posVisited[action.SourcePos] = true
			counts[action.Piece]++
			if !posInSlice(queenNeighbours, action.SourcePos) {
				countsNotQueenNeighbours[action.Piece]++
			}
		}
	}
	for _, piece := range Pieces {
		f[idx+2*(int(piece)-1)] = float32(counts[piece])
		f[idx+1+2*(int(piece)-1)] = float32(countsNotQueenNeighbours[piece])
	}
}

func posInSlice(slice []Pos, p Pos) bool {
	for _, sPos := range slice {
		if p == sPos {
			return true
		}
	}
	return false
}

func fNumThreateningMoves(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	player := b.NextPlayer
	opponent := b.OpponentPlayer()
	if def.FId == F_OPP_NUM_CAN_MOVE {
		player, opponent = opponent, player
	}
	actions := b.Derived.PlayersActions[player]
	f[idx] = 0
	f[idx+1] = 0
	if b.Available(opponent, QUEEN) > 0 {
		// Queen not yet set up.
		return
	}

	// Add
	freeOppQueenNeighbors := b.Derived.QueenPos[opponent].Neighbours()
	usedPieces := make(map[Pos]bool)
	usedPositions := make([]Pos, 0, len(freeOppQueenNeighbors))
	canPlaceAroundQueen := false
	for _, action := range actions {
		if !posInSlice(freeOppQueenNeighbors, action.TargetPos) ||
			posInSlice(freeOppQueenNeighbors, action.SourcePos) {
			continue
		}
		if action.Move {
			if !usedPieces[action.SourcePos] {
				// Number of pieces that can reach around opponent's queen.
				usedPieces[action.SourcePos] = true
				f[idx]++
			}
		} else {
			// Placement can happen when there is a bettle on top of the
			// opponent Queen.
			canPlaceAroundQueen = true
			continue
		}
		if !posInSlice(usedPositions, action.TargetPos) {
			// Number of positions around opponent's queen that can be reached.
			usedPositions = append(usedPositions, action.TargetPos)
			f[idx+1]++
		}
	}
	if canPlaceAroundQueen {
		// In this case any of the available pieces for placement can
		// be put around the Queen.
		f[idx] += float32(TOTAL_PIECES_PER_PLAYER - b.Derived.NumPiecesOnBoard[player])
	}
}

func fNumToDraw(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	f[idx] = float32(b.MaxMoves - b.MoveNumber + 1)

}

func fNumSingle(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	player := b.NextPlayer
	opponent := b.OpponentPlayer()
	f[idx] = float32(b.Derived.Singles[player])
	f[idx+1] = float32(b.Derived.Singles[opponent])
}

func fQueenIsCovered(b *Board, def *FeatureDef, f []float32) {
	idx := def.VecIndex
	player := b.NextPlayer
	opponent := b.OpponentPlayer()
	for ii := 0; ii < 2; ii++ {
		pos := b.Derived.QueenPos[player]
		posPlayer, _, _ := b.PieceAt(pos)
		if posPlayer != player {
			f[idx+ii] = 1.0
		} else {
			f[idx+ii] = 0.0
		}
		// Invert players selection.
		player, opponent = opponent, player
	}
}
