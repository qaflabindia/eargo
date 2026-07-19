package ear

// signatures is the catalogue of concrete reasoning tasks behind EAR's
// judgment-laden stages -- each a typed Signature[In, Out] (see
// signature.go) whose input and output structs declare the model's fields.
// Call one with out, err := Sig.Run(ctx, lm, In{...}); out is a typed value.
// This ports the Python signatures.py catalogue; the subset here is what the
// runtime uses, and the rest follow the same shape.

// -- policy governance ------------------------------------------------------

type PolicyComplianceIn struct {
	Statement string         `ear:"policy_statement,The policy, written in plain English"`
	Context   map[string]any `ear:"context,The intent's context values relevant to the policy"`
}
type PolicyComplianceOut struct {
	Complies  bool   `ear:"complies,True if the context satisfies the policy, False if it violates it"`
	Rationale string `ear:"rationale,One sentence explaining the judgment"`
}

// JudgePolicyCompliance judges a policy statement against a context.
var JudgePolicyCompliance = Signature[PolicyComplianceIn, PolicyComplianceOut]{
	Instruction: "Decide whether the given context complies with a written policy statement, the way a " +
		"careful compliance reviewer would: read the policy in plain English, check the context against it, " +
		"and explain your reasoning in one sentence.",
}

// -- deliberation -----------------------------------------------------------

type ReasonIn struct {
	Intent       string         `ear:"intent,The natural-language intent to resolve"`
	Context      map[string]any `ear:"context,Structured context relevant to the intent"`
	Capabilities string         `ear:"capabilities,The stacked personas and skill prompts composed for this intent"`
}
type ReasonOut struct {
	Decision string `ear:"decision,The concrete decision reached, with a brief justification"`
}

// ReasonAboutIntent resolves an intent into a concrete decision.
var ReasonAboutIntent = Signature[ReasonIn, ReasonOut]{
	Instruction: "Resolve an intent into a final, concrete decision given its context. Reason as the assembled " +
		"capabilities: the persona instructions and the stacked skill prompts describe who is acting and how -- " +
		"follow them when reaching the decision.",
}

// -- discovery / selection / scheduling / delegation ------------------------

type DiscoverIn struct {
	IntentText         string `ear:"intent_text"`
	AvailableProcesses string `ear:"available_processes,One 'name: description' pair per line"`
}
type DiscoverOut struct {
	RelevantProcessNames []string `ear:"relevant_process_names,Names of the relevant processes, most relevant first"`
}

// DiscoverRelevantProcesses ranks processes by relevance to an intent.
var DiscoverRelevantProcesses = Signature[DiscoverIn, DiscoverOut]{
	Instruction: "Identify which of the runtime's registered processes are relevant to handling the given " +
		"intent, most relevant first.",
}

type SelectIn struct {
	IntentText         string `ear:"intent_text"`
	CandidateProcesses string `ear:"candidate_processes,One 'name: description' pair per line"`
}
type SelectOut struct {
	SelectedProcessNames []string `ear:"selected_process_names,Names of the processes to run, most relevant first"`
}

// SelectProcesses narrows discovered candidates to those to run.
var SelectProcesses = Signature[SelectIn, SelectOut]{
	Instruction: "Choose which of the candidate processes this cycle genuinely needs to handle the intent, most " +
		"relevant first. Choose only among the candidates; never invent one.",
}

type ScheduleIn struct {
	IntentText string `ear:"intent_text"`
	Workflows  string `ear:"workflows,One 'name: step summary' pair per line, in current order"`
}
type ScheduleOut struct {
	OrderedWorkflowNames []string `ear:"ordered_workflow_names,Every workflow name, in execution order"`
}

// ScheduleWorkflows orders a composed plan.
var ScheduleWorkflows = Signature[ScheduleIn, ScheduleOut]{
	Instruction: "Order the workflows for the intent at hand: prerequisites and information-producing workflows " +
		"first. Return every workflow name, none dropped.",
}

type DelegateIn struct {
	Steps    string `ear:"steps,One 'number: instruction' line per undelegated step"`
	Personas string `ear:"personas,One 'name: instructions and skills' line per available persona"`
}
type DelegateOut struct {
	Assignments []string `ear:"assignments,One 'number: persona name' item per step"`
}

// DelegateSteps assigns undelegated steps to personas.
var DelegateSteps = Signature[DelegateIn, DelegateOut]{
	Instruction: "Assign each undelegated step to the best-suited persona from the available pool, reading each " +
		"step's instruction against the personas' standing instructions and stacked skills.",
}

type RankSkillsIn struct {
	IntentText      string `ear:"intent_text"`
	AvailableSkills string `ear:"available_skills,One 'name: instruction' pair per line"`
}
type RankSkillsOut struct {
	RelevantSkillNames []string `ear:"relevant_skill_names,Names of the relevant skills, most relevant first"`
}

// RankRelevantSkills selects the skills relevant to an intent.
var RankRelevantSkills = Signature[RankSkillsIn, RankSkillsOut]{
	Instruction: "Identify which of a persona's skills are relevant to the intent at hand, most relevant first.",
}

// -- contracts --------------------------------------------------------------

type ContractConformanceIn struct {
	Contract string `ear:"contract,One '- name: meaning' line per declared field"`
	Data     string `ear:"data,One '- name: value' line per delivered field"`
}
type ContractConformanceOut struct {
	Conforms  bool   `ear:"conforms,True only if every delivered value honors its field's meaning"`
	Rationale string `ear:"rationale,One sentence explaining the judgment"`
}

// JudgeContractConformance judges filled data against field meanings.
var JudgeContractConformance = Signature[ContractConformanceIn, ContractConformanceOut]{
	Instruction: "Judge whether each delivered value honors the meaning of its declared field, the way a " +
		"reviewer checking a deliverable would, and explain your reasoning in one sentence.",
}

// -- recall / audit / explain -----------------------------------------------

type RecallIn struct {
	IntentText string `ear:"intent_text"`
	History    string `ear:"history,The full remembered context window"`
}
type RecallOut struct {
	RelevantContext string `ear:"relevant_context,Only the remembered facts relevant to this intent"`
}

// RecallRelevantMemory recalls the history relevant to an intent.
var RecallRelevantMemory = Signature[RecallIn, RecallOut]{
	Instruction: "From the runtime's remembered history, recall what is genuinely relevant to the intent at " +
		"hand -- prior decisions, amounts and outcomes that should inform this cycle -- and leave the rest " +
		"behind. Recall facts as they were recorded; never invent or embellish them.",
}

type AuditIn struct {
	Decision string `ear:"decision"`
	Evidence string `ear:"evidence,The basis, policies checked, plan and recalled memory behind the decision"`
}
type AuditOut struct {
	Assessment string `ear:"assessment,One or two sentences: supported or not, and any gap found"`
}

// AuditEvidence inspects a decision's evidence.
var AuditEvidence = Signature[AuditIn, AuditOut]{
	Instruction: "Inspect a decision's evidence the way an internal auditor would: check the decision against " +
		"its basis, the policies checked and the plan, and say whether the evidence supports the decision, " +
		"naming any gap or inconsistency plainly.",
}

type ExplainIn struct {
	Basis    string `ear:"basis,The evidentiary basis the decision rests on"`
	Decision string `ear:"decision"`
}
type ExplainOut struct {
	Explanation string `ear:"explanation,One or two plain-English sentences"`
}

// ExplainDecision writes a human-readable explanation.
var ExplainDecision = Signature[ExplainIn, ExplainOut]{
	Instruction: "Write a short, human-readable explanation of why a decision was reached, given the " +
		"evidentiary basis for it.",
}

// -- memory / adaptation ----------------------------------------------------

type SummarizeIn struct {
	History string `ear:"history"`
}
type SummarizeOut struct {
	Summary string `ear:"summary,A short paragraph"`
}

// SummarizeHistory compresses overflowed memory into a paragraph.
var SummarizeHistory = Signature[SummarizeIn, SummarizeOut]{
	Instruction: "Summarize execution history into a short paragraph, preserving any decisions, amounts and " +
		"outcomes that later reasoning might need.",
}

type DistillIn struct {
	ExperienceSummary string `ear:"experience_summary"`
}
type DistillOut struct {
	Insight string `ear:"insight,One sentence"`
}

// DistillInsight states one durable lesson from experience.
var DistillInsight = Signature[DistillIn, DistillOut]{
	Instruction: "State one durable lesson, in one sentence, from aggregated execution experience that should " +
		"bias future decisions.",
}
