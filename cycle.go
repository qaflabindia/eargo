package ear

import "context"

// Cycle is the mutable state threaded through one reasoning cycle's stages.
// Each Stage reads what earlier stages produced and writes its own result,
// so the pipeline is a data flow over one value rather than a straight line
// of hardcoded calls. A stage returning an error aborts the cycle.
type Cycle struct {
	Ctx      context.Context
	Runtime  *Runtime
	Intent   Intent
	Approval *ApprovalVerdict

	Candidates []*Process     // discover
	Selected   []*Process     // select
	Plan       []*Workflow    // compose + schedule
	Recalled   string         // recall
	Research   *Research      // research (RAG)
	Decision   any            // reason
	Data       map[string]any // formalize: conformant contract deliverable fields
	Evidence   *Evidence      // evidence
}

// Stage is one named, composable step of the cycle. The pipeline is a
// []Stage the Runtime executes in order; stages can be reordered, inserted,
// removed or swapped without touching the runtime loop. Each judged stage
// chooses its deterministic or LLM path from the Cycle's runtime, so the same
// pipeline serves both offline and model-bound runs.
type Stage interface {
	Name() string
	Run(c *Cycle) error
}

// defaultPipeline is the standard cycle: two governance gates around the
// discover→schedule planning stages, then delegate, recall, reason, and the
// evidence/explain/audit/memory tail. Stages are stateless and consult the
// runtime at Run time, so WithLM needs no different pipeline.
func defaultPipeline() []Stage {
	return []Stage{
		governStage{scope: "Policy"},
		discoverStage{},
		selectStage{},
		composeStage{},
		scheduleStage{},
		governStage{scope: "Workflow policy", workflow: true},
		delegateStage{},
		recallStage{},
		researchStage{},
		reasonStage{},
		formalizeStage{},
		evidenceStage{},
		explainStage{},
		auditStage{},
		memoryStage{},
	}
}

// PipelineNames returns the ordered stage names -- useful for inspection and
// for confirming a customized pipeline.
func (r *Runtime) PipelineNames() []string {
	names := make([]string, len(r.Pipeline))
	for i, s := range r.Pipeline {
		names[i] = s.Name()
	}
	return names
}
