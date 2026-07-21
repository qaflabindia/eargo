package main

import (
	"context"
	"fmt"
	"os"

	ear "github.com/qaflabindia/ear"
)

// cmdDemo assembles a small credit-risk stack in code and reasons two
// applications through it -- a zero-setup way to see a governed cycle end to
// end, deterministic (no model, no network).
func cmdDemo(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: ear demo")
		return exitError
	}
	guru := &ear.Persona{Name: "Credit Risk Guru", Instructions: "Underwrite conservatively."}
	guru.AddSkill(&ear.Skill{Name: "risk_grade", Prompt: "Combine the score tier and DTI band into a grade A-E."})

	workflow := &ear.Workflow{Name: "Underwriting Workflow"}
	workflow.AddStep("Band the credit profile and assign a risk grade.", guru)
	workflow.AddStep("Decide approve or decline against the grade.", guru)
	workflow.AddPolicy(&ear.Policy{
		Name:               "Loan Amount Cap",
		Statement:          "The loan must not exceed $75,000.",
		FallbackExpression: "loan_amount <= 75000",
	})

	process := &ear.Process{Name: "Underwriting", Description: "Underwrite a consumer loan application."}
	process.AddWorkflow(workflow)

	runtime := ear.NewRuntime("Credit Risk Runtime")
	runtime.AddProcess(process)
	runtime.AddPolicy(&ear.Policy{
		Name:               "Debt-to-Income Ceiling",
		Statement:          "The debt-to-income ratio must not exceed 0.43.",
		FallbackExpression: "debt_to_income <= 0.43",
	})

	fmt.Println("== A compliant application ==")
	demoReason(runtime, ear.NewIntent("Underwrite a $20,000 consumer loan application",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28, "credit_score": 742.0}))

	fmt.Println("\n== An application that trips a policy ==")
	demoReason(runtime, ear.NewIntent("Underwrite a $90,000 consumer loan application",
		map[string]any{"loan_amount": 90000.0, "debt_to_income": 0.30, "credit_score": 705.0}))
	return exitDecided
}

func demoReason(runtime *ear.Runtime, intent ear.Intent) {
	decision, err := runtime.Reason(context.Background(), intent, nil)
	classifyOutcome(runtime, decision, err).print(os.Stdout)
}
