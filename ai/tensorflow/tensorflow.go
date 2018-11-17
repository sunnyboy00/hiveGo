package tensorflow

// Google's support for Tensorflow in Go is still lacking. To get the Tensorlow
// protobuffers needed compiled for go, do the following:
//
// 1) Install the Go Proto tool support. Details here:
//
// https://developers.google.com/protocol-buffers/docs/reference/go-generated
//
// I did the following:
//      go get github.com/golang/protobuf/proto
//      go get github.com/golang/protobuf/protoc-gen-go
//
// Have protoc installed (sudo apt install protobuf-compiler)
//
// 2) Get Tensorflow proto definitions (.proto files):
//
//      (From a directory called ${REPOS})
//      git clone git@github.com:tensorflow/tensorflow.git
//
// 3) Compile protos to Go:
//      ${REPOS} -> where you got the tensorflow sources in (2)
//      ${GOSRC} -> your primary location of Go packages, typically the first entry in GOPATH.
//      for ii in config.proto debug.proto cluster.proto rewriter_config.proto ; do
//        protoc --proto_path=${REPOS}/tensorflow --go_out=${GOSRC}/src \
//          ${REPOS}/tensorflow/tensorflow/core/protobuf/${ii}
//      done
//      protoc --proto_path=${REPOS}/tensorflow --go_out=${GOSRC}/src \
//          ${REPOS}/tensorflow/tensorflow/core/framework/*.proto
//
//      You can convert other protos as needed -- yes, unfortunately I only need config.proto
//      but had to manually track the dependencies ... :(
import (
	"fmt"
	"github.com/golang/protobuf/proto"
	tfconfig "github.com/tensorflow/tensorflow/tensorflow/go/core/protobuf"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/golang/glog"
	"github.com/janpfeifer/hiveGo/ai"
	"github.com/janpfeifer/hiveGo/ai/players"
	. "github.com/janpfeifer/hiveGo/state"
	tf "github.com/tensorflow/tensorflow/tensorflow/go"
)

// Set this to true to force to use CPU, even when GPU is avaialble.
var CpuOnly = false

const (
	INTER_OP_PARALLELISM       = 4
	INTRA_OP_PARALLELISM       = 4
	GPU_MEMORY_FRACTION_TO_USE = 0.5
)

type Scorer struct {
	Basename    string
	graph       *tf.Graph
	sessionPool []*tf.Session
	sessionTurn int // Rotate among the sessions from the pool.
	mu          sync.Mutex

	BoardFeatures, BoardLabels    tf.Output
	BoardPredictions, BoardLosses tf.Output

	ActionsBoardIndices, ActionsFeatures             tf.Output
	ActionsSourceCenter, ActionsSourceNeighbourhood  tf.Output
	ActionsTargetCenter, ActionsTargetNeighbourhood  tf.Output
	ActionsPredictions, ActionsLosses, ActionsLabels tf.Output

	LearningRate, CheckpointFile, TotalLoss tf.Output
	InitOp, TrainOp, SaveOp, RestoreOp      *tf.Operation

	version int // Uses the number of input features used.
}

// Data used for parsing of player options.
type ParsingData struct {
	UseTensorFlow, ForceCPU bool
	SessionPoolSize         int
}

func NewParsingData() (data interface{}) {
	return &ParsingData{SessionPoolSize: 1}
}

func FinalizeParsing(data interface{}, player *players.SearcherScorerPlayer) {
	d := data.(*ParsingData)
	if d.UseTensorFlow {
		player.Learner = New(player.ModelFile, d.SessionPoolSize, d.ForceCPU)
		player.Scorer = player.Learner
	}
}

func ParseParam(data interface{}, key, value string) {
	d := data.(*ParsingData)
	if key == "tf" {
		d.UseTensorFlow = true
	} else if key == "tf_cpu" {
		d.ForceCPU = true
	} else if key == "tf_session_pool_size" {
		var err error
		d.SessionPoolSize, err = strconv.Atoi(value)
		if err != nil {
			log.Panicf("Invalid parameter tf_session_pool_size=%s: %v", value, err)
		}
		if d.SessionPoolSize < 1 {
			log.Panicf("Invalid parameter tf_session_pool_size=%s, it must be > 0", value)
		}
	} else {
		log.Panicf("Unknown parameter '%s=%s' passed to tensorflow module.", key, value)
	}
}

func init() {
	players.RegisterPlayerParameter("tf", "tf", NewParsingData, ParseParam, FinalizeParsing)
	players.RegisterPlayerParameter("tf", "tf_cpu", NewParsingData, ParseParam, FinalizeParsing)
	players.RegisterPlayerParameter("tf", "tf_session_pool_size", NewParsingData, ParseParam, FinalizeParsing)
}

var dataTypeMap = map[tf.DataType]string{
	tf.Float:  "tf.float32",
	tf.Double: "tf.float64",
	tf.Int32:  "tf.int32",
	tf.Int64:  "tf.int64",
	tf.String: "tf.string",
}

func dataType(t tf.Output) string {
	dt := t.DataType()
	str, ok := dataTypeMap[dt]
	if !ok {
		return fmt.Sprintf("type_%d?", int(dt))
	}
	return str
}

// New creates a new Scorer by reading model's graph `basename`.pb,
// and checkpoints from `basename`.checkpoint
func New(basename string, sessionPoolSize int, forceCPU bool) *Scorer {
	// Load graph definition (as bytes) and import into current graph.
	graphDefFilename := fmt.Sprintf("%s.pb", basename)
	graphDef, err := ioutil.ReadFile(graphDefFilename)
	if err != nil {
		log.Panicf("Failed to read %q: %v", graphDefFilename, err)
	}

	// Create the one graph and sessions we will use all time.
	graph := tf.NewGraph()

	if err = graph.Import(graphDef, ""); err != nil {
		log.Fatal("Invalid GraphDef? read from %s: %v", graphDefFilename, err)
	}

	absBasename, err := filepath.Abs(basename)
	if err != nil {
		log.Panicf("Unknown absolute path for %s: %v", basename, err)
	}

	t0 := func(tensorName string) (to tf.Output) {
		op := graph.Operation(tensorName)
		if op == nil {
			log.Fatalf("Failed to find tensor [%s]", tensorName)
		}
		return op.Output(0)
	}
	op := func(tensorName string) *tf.Operation {
		return graph.Operation(tensorName)
	}

	s := &Scorer{
		Basename:    absBasename,
		graph:       graph,
		sessionPool: createSessionPool(graph, sessionPoolSize, forceCPU),

		// Board tensors.
		BoardFeatures:    t0("board_features"),
		BoardLabels:      t0("board_labels"),
		BoardPredictions: t0("board_predictions"),
		BoardLosses:      t0("board_losses"),

		// Actions related tensors.
		ActionsBoardIndices:        t0("actions_board_indices"),
		ActionsFeatures:            t0("actions_features"),
		ActionsSourceCenter:        t0("actions_source_center"),
		ActionsSourceNeighbourhood: t0("actions_source_neighbourhood"),
		ActionsTargetCenter:        t0("actions_target_center"),
		ActionsTargetNeighbourhood: t0("actions_target_neighbourhood"),
		ActionsPredictions:         t0("actions_predictions"),
		ActionsLosses:              t0("actions_losses"),
		ActionsLabels:              t0("actions_labels"),

		// Global parameters.
		LearningRate:   t0("learning_rate"),
		CheckpointFile: t0("save/Const"),
		TotalLoss:      t0("mean_loss"),

		// Ops.
		InitOp:    op("init"),
		TrainOp:   op("train"),
		SaveOp:    op("save/control_dependency"),
		RestoreOp: op("save/restore_all"),
	}

	// Notice there must be a bug in the library that prevents it from taking
	// tf.int32.
	glog.V(2).Infof("ActionsBoardIndices: type=%s", dataType(s.ActionsBoardIndices))

	// Set version to the size of the input.
	s.version = int(s.BoardFeatures.Shape().Size(1))
	glog.V(1).Infof("TensorFlow model's version=%d", s.version)

	// Either restore or initialize the network.
	cpIndex, _ := s.CheckpointFiles()
	if _, err := os.Stat(cpIndex); err == nil {
		glog.Infof("Loading model from %s", s.CheckpointBase())
		err = s.Restore()
		if err != nil {
			log.Panicf("Failed to load checkpoint from file %s: %v", s.CheckpointBase(), err)
		}
	} else if os.IsNotExist(err) {
		glog.Infof("Initializing model randomly, since %s not found", s.CheckpointBase())
		err = s.Init()
		if err != nil {
			log.Panicf("Failed to initialize model: %v", err)
		}
	} else {
		log.Panicf("Cannot checkpoint file %s: %v", s.CheckpointBase(), err)
	}

	return s
}

func createSessionPool(graph *tf.Graph, size int, forceCPU bool) (sessions []*tf.Session) {
	gpuMemFractionLeft := GPU_MEMORY_FRACTION_TO_USE
	for ii := 0; ii < size; ii++ {
		sessionOptions := &tf.SessionOptions{}
		var config tfconfig.ConfigProto
		if forceCPU || CpuOnly {
			// TODO this doesn't work .... :(
			// Instead use:
			//    export CUDA_VISIBLE_DEVICES=-1
			// Before starting the program.
			config.DeviceCount = map[string]int32{"GPU": 0}
		} else {
			config.GpuOptions = &tfconfig.GPUOptions{}
			config.GpuOptions.PerProcessGpuMemoryFraction = gpuMemFractionLeft / float64(size-ii)
			gpuMemFractionLeft -= config.GpuOptions.PerProcessGpuMemoryFraction
		}
		config.InterOpParallelismThreads = INTER_OP_PARALLELISM
		config.IntraOpParallelismThreads = INTRA_OP_PARALLELISM
		data, err := proto.Marshal(&config)
		if err != nil {
			log.Panicf("Failed to serialize tf.ConfigProto: %v", err)
		}
		sessionOptions.Config = data
		sess, err := tf.NewSession(graph, sessionOptions)
		if err != nil {
			log.Panicf("Failed to create tensorflow session: %v", err)
		}
		if ii == 0 {
			devices, _ := sess.ListDevices()
			glog.Infof("List of available devices: %v", devices)
		}
		sessions = append(sessions, sess)
	}
	return
}

func (s *Scorer) String() string {
	return fmt.Sprintf("TensorFlow model in '%s'", s.Basename)
}

func (s *Scorer) NextSession() (sess *tf.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess = s.sessionPool[s.sessionTurn]
	s.sessionTurn = (s.sessionTurn + 1) % len(s.sessionPool)
	return
}

func (s *Scorer) CheckpointBase() string {
	return fmt.Sprintf("%s.checkpoint", s.Basename)
}

func (s *Scorer) CheckpointFiles() (string, string) {
	return fmt.Sprintf("%s.checkpoint.index", s.Basename), fmt.Sprintf("%s.checkpoint.data-00000-of-00001", s.Basename)

}

func (s *Scorer) Restore() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessionPool {
		t, err := tf.NewTensor(s.CheckpointBase())
		if err != nil {
			log.Panicf("Failed to create tensor: %v", err)
		}
		feeds := map[tf.Output]*tf.Tensor{
			s.CheckpointFile: t,
		}
		_, err = sess.Run(feeds, nil, []*tf.Operation{s.RestoreOp})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Scorer) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessionPool {
		_, err := sess.Run(nil, nil, []*tf.Operation{s.InitOp})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Scorer) Version() int {
	return s.version
}

func (s *Scorer) Score(b *Board) (score float32, actionProbs []float32) {
	boards := []*Board{b}
	scores, actionProbsBatch := s.BatchScore(boards)
	return scores[0], actionProbsBatch[0]
}

// Quick utility to create a tensor out of value. Dies if there is an error.
func mustTensor(value interface{}) *tf.Tensor {
	tensor, err := tf.NewTensor(value)
	if err != nil {
		log.Panicf("Cannot convert to tensor: %v", err)
	}
	return tensor
}

func (s *Scorer) buildFeeds(boards []*Board) (feeds map[tf.Output]*tf.Tensor, totalNumActions int) {
	totalNumActions = 0
	for _, board := range boards {
		totalNumActions += len(board.Derived.Actions)
	}
	glog.V(2).Infof("BatchScore: #boards, #actions=[%d, %d]", len(boards), totalNumActions)

	// Initialize Go objects, that need to be copied to tensors.
	boardFeatures := make([][]float32, len(boards))
	actionsBoardIndices := make([]int64, 0, totalNumActions) // Go tensorflow implementation is broken for int32.
	actionsFeatures := make([][1]float32, 0, totalNumActions)
	actionsSourceCenter := make([][]float32, 0, totalNumActions)
	actionsSourceNeighbourhood := make([][6][]float32, 0, totalNumActions)
	actionsTargetCenter := make([][]float32, 0, totalNumActions)
	actionsTargetNeighbourhood := make([][6][]float32, 0, totalNumActions)

	// Generate features in Go slices.
	for boardIdx, board := range boards {
		boardFeatures[boardIdx] = ai.FeatureVector(board, s.version)
		for _, action := range board.Derived.Actions {
			af := ai.NewActionFeatures(board, action, s.version)
			actionsBoardIndices = append(actionsBoardIndices, int64(boardIdx))
			actionsFeatures = append(actionsFeatures, [1]float32{af.Move})
			actionsSourceCenter = append(actionsSourceCenter, af.SourceFeatures.Center)
			actionsSourceNeighbourhood = append(actionsSourceNeighbourhood,
				af.SourceFeatures.Sections)
			actionsTargetCenter = append(actionsTargetCenter, af.TargetFeatures.Center)
			actionsTargetNeighbourhood = append(actionsTargetNeighbourhood,
				af.TargetFeatures.Sections)

		}
	}

	// Convert Go slices to tensors.
	feeds = map[tf.Output]*tf.Tensor{
		s.BoardFeatures:              mustTensor(boardFeatures),
		s.ActionsBoardIndices:        mustTensor(actionsBoardIndices),
		s.ActionsFeatures:            mustTensor(actionsFeatures),
		s.ActionsSourceCenter:        mustTensor(actionsSourceCenter),
		s.ActionsSourceNeighbourhood: mustTensor(actionsSourceNeighbourhood),
		s.ActionsTargetCenter:        mustTensor(actionsTargetCenter),
		s.ActionsTargetNeighbourhood: mustTensor(actionsTargetNeighbourhood),
	}
	return
}

func (s *Scorer) BatchScore(boards []*Board) (scores []float32, actionProbsBatch [][]float32) {
	if len(boards) == 0 {
		log.Panicf("Received empty list of boards to score.")
	}

	// Build feeds to TF model.
	feeds, totalNumActions := s.buildFeeds(boards)
	fetches := []tf.Output{s.BoardPredictions}
	if totalNumActions > 0 {
		fetches = append(fetches, s.ActionsPredictions)
	}

	// Evaluate: at most one evaluation at a same time.
	if glog.V(2) {
		glog.V(2).Infof("Feeded tensors: ")
		for to, tensor := range feeds {
			glog.V(2).Infof("\t%s: %v", to.Op.Name(), tensor.Shape())
		}
	}

	sess := s.NextSession()
	results, err := sess.Run(feeds, fetches, nil)
	if err != nil {
		log.Panicf("Prediction failed: %v", err)
	}

	// Copy over resulting tensors.
	scores = results[0].Value().([]float32)
	if len(scores) != len(boards) {
		log.Panicf("Expected %d scores (=number of boards given), got %d",
			len(boards), len(scores))
	}

	actionProbsBatch = make([][]float32, len(boards))
	if totalNumActions > 0 {
		allActionsProbs := results[1].Value().([]float32)
		if len(allActionsProbs) != totalNumActions {
			log.Panicf("Total probabilities returned was %d, wanted %d",
				len(allActionsProbs), totalNumActions)
		}
		if len(allActionsProbs) != totalNumActions {
			log.Panicf("Expected %d actions (from %d boards), got %d",
				totalNumActions, len(boards), len(allActionsProbs))
		}
		for boardIdx, board := range boards {
			actionProbsBatch[boardIdx] = allActionsProbs[:board.NumActions()]
			allActionsProbs = allActionsProbs[len(board.Derived.Actions):]
			if len(actionProbsBatch[boardIdx]) != board.NumActions() {
				log.Panicf("Got %d probabilities for %d actions!?", len(actionProbsBatch[boardIdx]),
					board.NumActions())
			}
		}
	}
	return
}

func (s *Scorer) Learn(boards []*Board, boardLabels []float32, actionsLabels [][]float32, learningRate float32, steps int) (loss float32) {
	if len(s.sessionPool) > 1 {
		log.Panicf("SessionPool doesn't support saving. You probably should use sessionPoolSize=1 in this case.")
	}

	feeds, totalNumActions := s.buildFeeds(boards)
	if len(boards) == 0 {
		log.Panicf("Received empty list of boards to learn.")
	}

	// Feed also the labels.
	actionsSparseLabels := make([]float32, 0, totalNumActions)
	for ii, labels := range actionsLabels {
		if len(labels) > 0 {
			if len(labels) != boards[ii].NumActions() {
				log.Panicf("%d actionsLabeles given to board, but there are %d actions", len(labels), boards[ii].NumActions())
			}
			actionsSparseLabels = append(actionsSparseLabels, labels...)
		}
	}
	if len(actionsSparseLabels) != totalNumActions {
		log.Panicf("Expected %d actions labels in total, got %d", totalNumActions, len(actionsSparseLabels))
	}
	feeds[s.BoardLabels] = mustTensor(boardLabels)
	feeds[s.ActionsLabels] = mustTensor(actionsSparseLabels)
	feeds[s.LearningRate] = mustTensor(learningRate)

	// Loop over steps.
	for step := 0; step < steps; step++ {
		if _, err := s.sessionPool[0].Run(feeds, nil, []*tf.Operation{s.TrainOp}); err != nil {
			log.Panicf("TensorFlow trainOp failed: %v", err)
		}
	}

	fetches := []tf.Output{s.TotalLoss}
	results, err := s.sessionPool[0].Run(feeds, fetches, nil)
	if err != nil {
		log.Panicf("Loss evaluation failed: %v", err)
	}
	return results[0].Value().(float32)
}

func (s *Scorer) Save() {
	if len(s.sessionPool) > 1 {
		log.Panicf("SessionPool doesn't support saving. You probably should use sessionPoolSize=1 in this case.")
	}

	// Backup previous checkpoint.
	index, data := s.CheckpointFiles()
	if _, err := os.Stat(index); err == nil {
		if err := os.Rename(index, index+"~"); err != nil {
			glog.Errorf("Failed to backup %s to %s~: %v", index, index, err)
		}
		if err := os.Rename(data, data+"~"); err != nil {
			glog.Errorf("Failed to backup %s to %s~: %v", data, data, err)
		}
	}

	t, err := tf.NewTensor(s.CheckpointBase())
	if err != nil {
		log.Panicf("Failed to create tensor: %v", err)
	}
	feeds := map[tf.Output]*tf.Tensor{s.CheckpointFile: t}
	if _, err := s.sessionPool[0].Run(feeds, nil, []*tf.Operation{s.SaveOp}); err != nil {
		log.Panicf("Failed to checkpoint (save) file to %s: %v", s.CheckpointBase(), err)
	}
}
