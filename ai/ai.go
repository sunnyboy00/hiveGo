package ai

import (
	. "github.com/janpfeifer/hiveGo/state"
)

// Scorer returns two scores:
//   value: how likely the current player is to win, in the form of
//     a score from +10 and -10.
//   policy: Probability for each of the action. This is optional, and
//     some models may not return it.
type Scorer interface {
	Score(board *Board) (score float32, actionProbs []float32)

	// Version returns the version of the Scorer: usually the number of features
	// used -- so system can maintain backwards compatibility.
	Version() int
}

// BatchScorer is a Scorer that also handles batches.
type BatchScorer interface {
	Scorer

	// BatchScore aggregate scoring in batches -- presumable more efficient.
	BatchScore(boards []*Board) (scores []float32, actionProbsBatch [][]float32)
}

type LearnerScorer interface {
	BatchScorer

	// actionLabels is the index of the action that was taken. It should be -1 if there are no valid
	// actons, that is, when `len(board.Derived.Actions) == 0`.
	Learn(boards []*Board, boardLabels []float32, actionsLabels [][]float32,
		learningRate float32, steps int) (loss float32)
	Save()
	String() string
}

// Trivial implementation of a BatchScorer, wity no efficiency gains.
type BatchScorerWrapper struct {
	Scorer
}

func (s BatchScorerWrapper) BatchScore(boards []*Board) (scores []float32, actionProbsBatch [][]float32) {
	scores = make([]float32, len(boards))
	actionProbsBatch = nil
	for ii, board := range boards {
		var actionProbs []float32
		scores[ii], actionProbs = s.Score(board)
		if actionProbs != nil {
			if actionProbsBatch == nil {
				actionProbsBatch = make([][]float32, len(boards))
			}
			actionProbsBatch[ii] = actionProbs
		}
	}
	return
}

// Returns weather it's the end of the game, and the hard-coded score of a win/loss/draw
// for the current player if it is finished.
func EndGameScore(b *Board) (isEnd bool, score float32) {
	if !b.IsFinished() {
		return false, 0
	}
	if b.Draw() {
		return true, 0
	}
	if b.Derived.Wins[b.NextPlayer] {
		// Current player wins.
		return true, 10.0
	}
	// Opponent player wins.
	return true, -10.0
}

// Returns a slice of float32 with one element set to 1, and all others to 0.
func OneHotEncoding(total, selected int) (vec []float32) {
	vec = make([]float32, total)
	if total > 0 {
		vec[selected] = 1
	}
	return
}
