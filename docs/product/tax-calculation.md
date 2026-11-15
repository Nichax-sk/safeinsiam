# Tax calculation requirements

Status: M11 draft. **No tax value in this document is verified for Tax Year 2026.** Every rate, cap and condition must be checked against official Thai Revenue Department sources before implementation (TAX-02). The calculator produces estimates only; it is not tax advice.

## 1. Scope

- TAX-01 (MUST): The MVP supports Thai personal income tax for **Tax Year 2026** (B.E. 2569), the calendar year 1 January to 31 December 2026, whose return is normally filed in early 2027 `[VERIFY: official]` (D6).
- The MVP targets an individual Thai tax resident whose main income is employment income (Revenue Code section 40(1)). Whether other income types are in scope, and how far, is OQ-15. Until it is decided, the calculator MUST say which income types it does not handle.
- Out of scope: corporate tax, non-resident taxation, foreign-sourced income rules, joint spouse filing, withholding-tax refunds, and e-filing.

## 2. Requirements

| ID | Requirement | Priority |
|---|---|---|
| TAX-02 | Before any 2026 rule set is published, every value and condition in it is checked against an official source in section 5, by a named reviewer, and the reference is recorded in the `TaxRule` `source_refs`. | MUST |
| TAX-03 | Rules are versioned per tax year (`TaxRule`: `tax_year`, `version`, `status`). Published versions are immutable; a correction is a new version. | MUST |
| TAX-04 | Every result names the `tax_year` and `rule_version` it used. | MUST |
| TAX-05 | Inputs: income (by type supported), existing deductions and allowances, and insurance premiums (from a catalog product or entered by hand). | MUST |
| TAX-06 | Outputs: estimated assessable income, expense deduction, allowances and deductions, taxable (net) income, and tax payable, each **before** and **after** the eligible premiums, and the difference. | MUST |
| TAX-07 | A premium counts only when (a) the rule set has a matching deduction category, (b) the product's `TaxEligibility` is `verified` against the currently published rule version for that tax year (TAX-17), or the user has declared the category for a policy they hold, and (c) the category's conditions and caps allow it, including conditions about another person (TAX-16). Nothing is deductible by default. | MUST |
| TAX-08 | Personal accident premiums are not treated as deductible unless a verified rule says otherwise. Critical illness premiums count only if a verified rule and verified product eligibility say so (OQ-10). | MUST |
| TAX-09 | The result explains each premium and each claimed allowance: counted, counted as user-declared, capped (with the cap), not eligible, eligibility unconfirmed, condition not met, or condition unconfirmed (TAX-16). | MUST |
| TAX-10 | Shared caps (for example combined life and health caps, or a combined retirement-savings cap) are applied in a documented order, so the same inputs always give the same result. | MUST |
| TAX-11 | All amounts are integer satang. Rounding (for example of percentage-based caps and of tax per bracket) follows the verified rules; until verified, the rounding rule is OQ-21. | MUST |
| TAX-12 | Every result shows the estimate disclaimer (section 6) and the list of limitations that apply to the inputs. | MUST |
| TAX-13 | Explanations of each deduction category are available in Thai and English, with a link to the official source. | MUST |
| TAX-14 | The calculation is deterministic and covered by worked examples that a reviewer has checked against official material (golden tests in a later milestone). | MUST |
| TAX-15 | Inputs are not stored in the MVP (OQ-05) and never logged (SEC-05). | MUST |
| TAX-16 | **Conditions about another person.** Some deductions depend on facts about someone other than the taxpayer. Candidates: the spouse allowance and the spouse life-insurance premium (spouse has no income); the parents' health-insurance premium and the parental allowance (each parent's income below a threshold, and possibly age and sharing between siblings) `[VERIFY: official]`. Each condition the rule set requires for that person has one of three answer states, and the deduction is decided as follows. **Any `false`** (condition not met): the deduction does **not** count, its status is `condition_not_met`, and the result explains which condition was not met. **Otherwise any `null` or missing** ("don't know" or unanswered): the deduction does **not** count, its status is `condition_unconfirmed`, and the result names each missing answer. **All `true`** (condition met, as declared by the user): the deduction is then checked against every other eligibility rule and cap and may still be capped or not eligible; if it counts, its status is `counted_user_declared`. A `true` answer is always shown as declared by the user, never as verified by SafeInSiam, which cannot check another person's income. These rules apply both to the "after" figure and to allowances already in "before". Which questions are asked (yes/no or amounts), how parents are represented, and sibling sharing are OQ-26. | MUST |
| TAX-17 | **Eligibility is tied to a rule version.** A product's tax eligibility is verified against one specific published `TaxRule` version (`verified_against_rule_id`), not just a tax year. A verification counts in calculations only while that version is the currently published version for its tax year. | MUST |
| TAX-18 | **Rule version change within a tax year.** Publishing a new version for a tax year marks the previous version `superseded`. From then on, every eligibility verified against the superseded version is treated as `eligibility_unconfirmed` in calculations and shown publicly as "tax eligibility under review", until a reviewer re-verifies it against the new version. This status is derived when the data is read, from the rule version; it does not edit the product's published revision (INS-29). Re-verification records the reviewer, the time and the rule version, and is published as a new product revision. Whether unchanged deduction categories can be re-verified in bulk, and whether a new version may be published before affected products are re-verified, is OQ-27. | MUST |
| TAX-19 | Each result names, for every product premium it counted, the rule version and product revision whose eligibility it relied on, so the result can be reproduced. | SHOULD |

## 3. Calculation outline

The structure below is the usual Thai personal income tax flow. Each step's values come from the rule set.

1. **Assessable income** by income type.
2. **Expense deduction** by income type (for employment income, a percentage capped at a maximum).
3. **Allowances and deductions:** personal, spouse, children, parents, social security contributions, retirement savings, insurance premiums and others, each with its own conditions and caps.
4. **Taxable (net) income** = assessable income − expense deductions − allowances and deductions, floored at zero.
5. **Tax** from progressive brackets on taxable income.
6. Where the rules require an alternative minimum computation for certain non-employment income, compare and apply it `[VERIFY: official]` (only if OQ-15 brings that income into scope).

"Before" runs steps 1–5 without the premiums being evaluated; "after" runs them with the premiums. Premiums the user already holds can be part of "existing deductions"; the UI must make clear which premiums are being added (UX-09).

## 4. Candidate values to verify

These are **candidate values as understood for recent tax years**, listed to guide verification. They are not confirmed for 2026, may be out of date, and MUST NOT be implemented until TAX-02 is satisfied. Each line is `[VERIFY: official]`.

### 4.1 Progressive brackets (taxable income, THB)

| From | To | Candidate rate |
|---|---|---|
| 0 | 150,000 | exempt |
| 150,001 | 300,000 | 5% |
| 300,001 | 500,000 | 10% |
| 500,001 | 750,000 | 15% |
| 750,001 | 1,000,000 | 20% |
| 1,000,001 | 2,000,000 | 25% |
| 2,000,001 | 5,000,000 | 30% |
| over 5,000,000 | | 35% |

### 4.2 Selected items relevant to the MVP

| Item | Candidate rule |
|---|---|
| Employment income expense deduction | 50% of income, capped at 100,000 |
| Personal allowance | 60,000 |
| Social security contributions | Amount actually paid, within the statutory contribution ceiling |
| Life insurance premium (self) | Up to 100,000; policy term of at least 10 years; insurer licensed in Thailand; other conditions on cash-back benefits may apply |
| Health insurance premium (self) | Up to 25,000; combined with the life insurance premium deduction, capped at 100,000 |
| Health insurance premium for parents | Up to 15,000 in total, with conditions on the parents' income |
| Life insurance premium for a spouse without income | Up to 10,000 |
| Pension (annuity) insurance premium | Up to 15% of assessable income, capped at 200,000; part of a combined cap with retirement funds (candidate 500,000); unused life-insurance allowance may extend it |
| Personal accident premium | No deduction known |
| Critical illness premium | May qualify under the health-insurance deduction only if the policy meets the health-insurance conditions; unknown (OQ-10) |

Temporary measures announced for a specific year (for example spending-linked deductions) are not part of the MVP unless a later decision adds them; the calculator lists them as not covered.

## 5. Official sources to verify

Each source must be checked in its current version for Tax Year 2026 and recorded by title, issuing body, number, date and URL.

| Source | Issuer | Used for |
|---|---|---|
| Revenue Code (ประมวลรัษฎากร), personal income tax provisions, including the income types (section 40), expense deductions and allowances (sections 42 bis–47 range) and rates (section 48) `[VERIFY: official]` section numbers | Revenue Department (rd.go.th) | Brackets, income types, expense deductions, allowances |
| Ministerial Regulations and Royal Decrees issued under the Revenue Code on income-tax exemptions and deductions | Ministry of Finance / Revenue Department | Insurance-premium deductions, pension insurance, caps |
| Notifications of the Director-General of the Revenue Department on income tax (ประกาศอธิบดีกรมสรรพากรเกี่ยวกับภาษีเงินได้) covering life insurance, health insurance (self and parents) and pension insurance conditions | Revenue Department | Eligibility conditions for each premium deduction |
| Personal income tax return forms and instructions for Tax Year 2026 (ภ.ง.ด.90 / ภ.ง.ด.91) and the Revenue Department's guidance on allowances | Revenue Department | Cross-check of the final allowance list and caps for the year |
| Cabinet resolutions or new laws in 2026 affecting the 2026 tax year | Revenue Department / Royal Gazette | Year-specific changes |
| Insurer certification that a product qualifies for a deduction | Each insurer | `TaxEligibility` evidence (not a substitute for the rules) |

## 6. Disclaimers

Text to be finalised with legal review (SEC-07). Required content, in Thai and English:

- The result is an estimate based on the information entered and the rule version shown.
- It is not tax advice and is not a guarantee of the tax payable or of any refund.
- Whether a premium is deductible depends on the policy and on the Revenue Department's conditions. The user should check with the insurer and the Revenue Department, or a qualified tax adviser.
- Situations the calculator does not cover, listed for the inputs given.
