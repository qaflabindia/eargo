# Skills

Prompts stacked into skills. Each heading names a skill; the prose beneath
it is the prompt the runtime reasons with. No code anywhere.

## band_credit_profile

Read the applicant's credit score, income and existing debt from the
intent's context. Band the credit score into poor, fair, good or excellent,
and the debt-to-income ratio into low, moderate or high.

## assign_risk_grade

Combine the score band and the debt-to-income band into a single risk grade
from A (strongest) to E (weakest), and say in one sentence why that grade
follows from the bands.

## decide_application

Given the risk grade, decide approve or decline. Approve grades A to C when
no policy is violated; decline grades D and E, naming the decisive factor.

## write_customer_note

Draft a short, courteous note to the applicant stating the decision and its
main reason, in plain English with no internal jargon.
