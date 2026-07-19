package ear

// signatures is the catalogue of concrete Judgments behind EAR's
// judgment-laden stages -- the natural-language reasoning tasks, each a
// Judgment value with typed input and output Fields. They mirror the Python
// package's signatures.py. Call one with sig.Run(ctx, lm, values). This port
// carries the subset the ported pipeline uses; the rest follow the same shape.
var (
	// JudgePolicyCompliance judges a policy statement against context.
	JudgePolicyCompliance = Judgment{
		Instruction: "Decide whether the given context complies with a written policy statement, the way a " +
			"careful compliance reviewer would: read the policy in plain English, check the context against " +
			"it, and explain your reasoning in one sentence.",
		Inputs: []Field{
			NewField("policy_statement", "The policy, written in plain English"),
			NewField("context", "The intent's context values relevant to the policy"),
		},
		Outputs: []Field{
			{Name: "complies", Desc: "True if the context satisfies the policy, False if it violates it", Kind: KindBool},
			{Name: "rationale", Desc: "One sentence explaining the judgment", Kind: KindText},
		},
	}

	// ReasonAboutIntent resolves an intent into a concrete decision.
	ReasonAboutIntent = Judgment{
		Instruction: "Resolve an intent into a final, concrete decision given its context. Reason as the " +
			"assembled capabilities: the persona instructions and the stacked skill prompts describe who is " +
			"acting and how -- follow them when reaching the decision.",
		Inputs: []Field{
			NewField("intent", "The natural-language intent to resolve"),
			NewField("context", "Structured context relevant to the intent"),
			NewField("capabilities", "The stacked personas and skill prompts composed for this intent"),
		},
		Outputs: []Field{{Name: "decision", Desc: "The concrete decision reached, with a brief justification", Kind: KindText}},
	}

	// DiscoverRelevantProcesses ranks processes by relevance to an intent.
	DiscoverRelevantProcesses = Judgment{
		Instruction: "Identify which of the runtime's registered processes are relevant to handling the given " +
			"intent, most relevant first.",
		Inputs: []Field{
			NewField("intent_text", ""),
			NewField("available_processes", "One 'name: description' pair per line"),
		},
		Outputs: []Field{{Name: "relevant_process_names", Desc: "Names of the relevant processes, most relevant first", Kind: KindList}},
	}

	// SelectProcesses narrows discovered candidates to those to run.
	SelectProcesses = Judgment{
		Instruction: "Choose which of the candidate processes this cycle genuinely needs to handle the intent, " +
			"most relevant first. Choose only among the candidates; never invent one.",
		Inputs: []Field{
			NewField("intent_text", ""),
			NewField("candidate_processes", "One 'name: description' pair per line"),
		},
		Outputs: []Field{{Name: "selected_process_names", Desc: "Names of the processes to run, most relevant first", Kind: KindList}},
	}

	// ScheduleWorkflows orders a composed plan.
	ScheduleWorkflows = Judgment{
		Instruction: "Order the workflows for the intent at hand: prerequisites and information-producing " +
			"workflows first. Return every workflow name, none dropped.",
		Inputs: []Field{
			NewField("intent_text", ""),
			NewField("workflows", "One 'name: step summary' pair per line, in current order"),
		},
		Outputs: []Field{{Name: "ordered_workflow_names", Desc: "Every workflow name, in execution order", Kind: KindList}},
	}

	// DelegateSteps assigns undelegated steps to personas.
	DelegateSteps = Judgment{
		Instruction: "Assign each undelegated step to the best-suited persona from the available pool, reading " +
			"each step's instruction against the personas' standing instructions and stacked skills.",
		Inputs: []Field{
			NewField("steps", "One 'number: instruction' line per undelegated step"),
			NewField("personas", "One 'name: instructions and skills' line per available persona"),
		},
		Outputs: []Field{{Name: "assignments", Desc: "One 'number: persona name' item per step", Kind: KindList}},
	}

	// RankRelevantSkills selects the skills relevant to an intent.
	RankRelevantSkills = Judgment{
		Instruction: "Identify which of a persona's skills are relevant to the intent at hand, most relevant first.",
		Inputs: []Field{
			NewField("intent_text", ""),
			NewField("available_skills", "One 'name: instruction' pair per line"),
		},
		Outputs: []Field{{Name: "relevant_skill_names", Desc: "Names of the relevant skills, most relevant first", Kind: KindList}},
	}

	// JudgeContractConformance judges filled data against field meanings.
	JudgeContractConformance = Judgment{
		Instruction: "Judge whether each delivered value honors the meaning of its declared field, the way a " +
			"reviewer checking a deliverable would, and explain your reasoning in one sentence.",
		Inputs: []Field{
			NewField("contract", "One '- name: meaning' line per declared field"),
			NewField("data", "One '- name: value' line per delivered field"),
		},
		Outputs: []Field{
			{Name: "conforms", Desc: "True only if every delivered value honors its field's meaning", Kind: KindBool},
			{Name: "rationale", Desc: "One sentence explaining the judgment", Kind: KindText},
		},
	}

	// RecallRelevantMemory recalls the history relevant to an intent.
	RecallRelevantMemory = Judgment{
		Instruction: "From the runtime's remembered history, recall what is genuinely relevant to the intent at " +
			"hand -- prior decisions, amounts and outcomes that should inform this cycle -- and leave the rest " +
			"behind. Recall facts as they were recorded; never invent or embellish them.",
		Inputs: []Field{
			NewField("intent_text", ""),
			NewField("history", "The full remembered context window"),
		},
		Outputs: []Field{{Name: "relevant_context", Desc: "Only the remembered facts relevant to this intent", Kind: KindText}},
	}

	// AuditEvidence inspects a decision's evidence.
	AuditEvidence = Judgment{
		Instruction: "Inspect a decision's evidence the way an internal auditor would: check the decision against " +
			"its basis, the policies checked and the plan, and say whether the evidence supports the decision, " +
			"naming any gap or inconsistency plainly.",
		Inputs: []Field{
			NewField("decision", ""),
			NewField("evidence", "The basis, policies checked, plan and recalled memory behind the decision"),
		},
		Outputs: []Field{{Name: "assessment", Desc: "One or two sentences: supported or not, and any gap found", Kind: KindText}},
	}

	// ExplainDecision writes a human-readable explanation.
	ExplainDecision = Judgment{
		Instruction: "Write a short, human-readable explanation of why a decision was reached, given the " +
			"evidentiary basis for it.",
		Inputs: []Field{
			NewField("basis", "The evidentiary basis the decision rests on"),
			NewField("decision", ""),
		},
		Outputs: []Field{{Name: "explanation", Desc: "One or two plain-English sentences", Kind: KindText}},
	}

	// SummarizeHistory compresses overflowed memory into a paragraph.
	SummarizeHistory = Judgment{
		Instruction: "Summarize execution history into a short paragraph, preserving any decisions, amounts and " +
			"outcomes that later reasoning might need.",
		Inputs:  []Field{NewField("history", "")},
		Outputs: []Field{{Name: "summary", Desc: "A short paragraph", Kind: KindText}},
	}

	// DistillInsight states one durable lesson from experience.
	DistillInsight = Judgment{
		Instruction: "State one durable lesson, in one sentence, from aggregated execution experience that should " +
			"bias future decisions.",
		Inputs:  []Field{NewField("experience_summary", "")},
		Outputs: []Field{{Name: "insight", Desc: "One sentence", Kind: KindText}},
	}
)
