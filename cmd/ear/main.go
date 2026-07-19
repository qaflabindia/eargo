// Command ear is a small demonstration of the Go port of EAR's
// deterministic runtime. It either loads a stacked-markdown directory and
// reasons a one-line intent through it, or -- with no arguments -- runs a
// built-in credit-risk stack assembled in code.
//
// Usage:
//
//	ear                                  # run the built-in demo stack
//	ear <stack-dir> "<intent>" [k=v ...] # load a markdown stack and reason
//
// Context facts are passed as key=value pairs and coerced to numbers /
// booleans the same way the markdown loader coerces them.
package main

import (
	"fmt"
	"os"
	"strings"

	ear "github.com/qaflabindia/ear"
)

func main() {
	if len(os.Args) < 2 {
		runBuiltin()
		return
	}
	dir := os.Args[1]
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, `usage: ear <stack-dir> "<intent>" [key=value ...]`)
		os.Exit(2)
	}
	runtime, err := ear.LoadRuntime(dir, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "load error:", err)
		os.Exit(1)
	}
	context := map[string]any{}
	for _, arg := range os.Args[3:] {
		if key, value, ok := strings.Cut(arg, "="); ok {
			context[strings.TrimSpace(key)] = ear.Coerce(value)
		}
	}
	reason(runtime, ear.NewIntent(os.Args[2], context))
}

func runBuiltin() {
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
	reason(runtime, ear.NewIntent("Underwrite a $20,000 consumer loan application",
		map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28, "credit_score": 742.0}))

	fmt.Println("\n== An application that trips a policy ==")
	reason(runtime, ear.NewIntent("Underwrite a $90,000 consumer loan application",
		map[string]any{"loan_amount": 90000.0, "debt_to_income": 0.30, "credit_score": 705.0}))
}

func reason(runtime *ear.Runtime, intent ear.Intent) {
	decision, err := runtime.Reason(intent, nil)
	if err != nil {
		fmt.Println("blocked:", err)
		return
	}
	fmt.Println("decision:", decision)
}
