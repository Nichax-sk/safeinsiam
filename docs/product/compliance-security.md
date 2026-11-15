# Compliance and security requirements

Status: M11 draft. **This document states requirements and questions. It contains no legal conclusions and does not claim that SafeInSiam complies with any law or regulation.** Items marked `[VERIFY: legal]` need a qualified Thai lawyer; items marked `[VERIFY: official]` need checking against an official source.

## 1. Regulatory position

| ID | Requirement |
|---|---|
| SEC-01 (MUST) | Before public launch, a qualified Thai lawyer reviews the product against insurance regulation, including the Life Insurance Act B.E. 2535 and Non-Life Insurance Act B.E. 2535 as amended, and Office of Insurance Commission (OIC, คปภ.) rules on brokers, agents, insurance advertising and online insurance activity `[VERIFY: legal]`. Launch is blocked until the review is complete and its findings are addressed (BR-13). |
| SEC-02 (MUST) | The review answers, at least: whether comparing products and showing indicative premiums, displaying contact channels, collecting contact requests (OQ-03), or receiving any payment from insurers (OQ-04) requires a broker or other licence, or a partnership with a licensed party `[VERIFY: legal]`. |
| SEC-02a (MUST) | SafeInSiam MUST NOT state or imply that it is an insurer, an insurance broker, an agent or a licensed adviser, or that it sells, underwrites or guarantees policies, unless and until it holds the relevant licence. Copy, metadata, domain names and ads are all covered. |
| SEC-02b (MUST) | Use of insurer names, logos and product names follows the insurers' terms or permission (OQ-11) `[VERIFY: legal]`. |
| SEC-02c (MUST) | Tax content is presented as general information and estimates. Whether any tax content needs extra disclaimers or qualifications is part of the legal review `[VERIFY: legal]`. |

## 2. Personal data (Thai PDPA)

The Personal Data Protection Act B.E. 2562 (2019) applies to personal data SafeInSiam collects `[VERIFY: legal]` for the specific obligations below.

| ID | Requirement |
|---|---|
| SEC-03 (MUST) | **Data minimisation.** Collect only what each feature needs. The MVP collects no national ID number, no health or medical information, no exact date of birth (age is enough for estimates), and no income tied to an identity. Answers about a spouse's or parent's income (TAX-16) are personal data about a third party; the minimum needed is decided in OQ-26. Health data and similar categories are sensitive data under the PDPA and need explicit consent; the MVP avoids collecting them. |
| SEC-04 (MUST) | **Lawful basis and notice.** Before any personal data is collected (for example a contact request), the lawful basis is documented and a bilingual, versioned privacy notice is shown. Consent, where it is the basis, is specific, not pre-ticked, recorded with notice version and time, and withdrawable `[VERIFY: legal]`. |
| SEC-05 (MUST) | **No personal data in logs.** Application logs never contain names, contact details, income, deductions, premiums entered by users, ages, request bodies or query strings. The existing access log already excludes bodies, query strings and headers; feature handlers keep it that way, and calculation inputs are never sent in URLs. |
| SEC-06 (MUST) | **Statelessness by default.** Tax calculation and comparison inputs are processed per request and not stored (OQ-05, OQ-06). Anything stored later needs a documented purpose, retention period and deletion process. |
| SEC-06a (MUST) | **Data subject rights.** Where personal data is stored (for example `QuoteRequest`), there is a process to handle access, correction, deletion and objection requests within the legal time limits `[VERIFY: legal]`. |
| SEC-06b (MUST) | **Retention.** Each stored personal-data item has a retention period and is deleted after it (`delete_after`). Periods are set with the legal review. |
| SEC-06c (MUST) | **Processors and transfers.** Any third party that processes personal data, and any transfer outside Thailand (for example hosting), is listed and assessed before launch `[VERIFY: legal]`. Hosting is not chosen yet. |
| SEC-06d (SHOULD) | **Data protection officer and breach response.** Whether a DPO is required, and the breach-notification procedure and time limits, are determined in the legal review `[VERIFY: legal]`. |
| SEC-06e (MUST) | **Analytics and cookies.** No analytics, tracking pixels or non-essential cookies in the MVP unless a later decision adds them with a consent mechanism. |

## 3. Security

| ID | Requirement |
|---|---|
| SEC-08 (MUST) | Existing backend protections stay: request ID, fixed security headers, strict CORS without credentials, `DecodeJSON` limits, sanitised database errors, panic recovery without value text. |
| SEC-09 (MUST) | Admin authentication and authorisation are designed and reviewed before any admin route is deployed (OQ-07). Requirements for that design: strong credentials or single sign-on, multi-factor authentication for admins, session expiry, CSRF protection if cookies are used, least-privilege roles, and an audit trail of admin actions (revisions cover catalog content). |
| SEC-10 (MUST) | Public write endpoints (calculations, comparisons, contact requests) are rate limited (OQ-19). |
| SEC-11 (MUST) | Production traffic uses TLS at the terminating proxy, with HSTS set there (CLAUDE.md: the app serves plain HTTP). |
| SEC-12 (MUST) | Admin text that is rendered publicly (product descriptions, notes) is rendered as text, not raw HTML, to prevent stored XSS. Any rich-text need is OQ-25. |
| SEC-13 (MUST) | Secrets stay out of the repository, logs and error messages, as today. |

## 4. Traceability

| ID | Requirement |
|---|---|
| SEC-14 (MUST) | Every published product names its sources with document and retrieval dates (INS-16, INS-17) and keeps an immutable revision history with who, when and why (INS-21). |
| SEC-15 (MUST) | Every tax rule set names its official sources and reviewer, and published versions are immutable (TAX-02, TAX-03). Every tax result names its rule version (TAX-04). |
| SEC-16 (SHOULD) | It is possible to reconstruct what a user would have seen for a product on a given date, from revisions and effective dates. |

## 5. Disclaimers

| ID | Requirement |
|---|---|
| SEC-07 (MUST) | Bilingual, versioned disclaimers, finalised with legal review, for: indicative premiums (not a quotation or offer; the insurer sets the actual premium after underwriting), tax estimates (not tax advice; no guaranteed outcome), data freshness (catalog data may change; check with the insurer), and SafeInSiam's role (SEC-02a). |
| SEC-07a (MUST) | Disclaimers appear next to the figures they qualify (UX-10) and are included in API responses that carry such figures (`disclaimer` object). |
