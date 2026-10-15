# ztd Commercial OEM License Agreement

This Commercial OEM License Agreement (the "Agreement") is entered into as of **[EFFECTIVE DATE]** (the
"Effective Date") between:

- **Lateralus Labs, LLC**, an Oregon limited liability company located in Portland, Oregon ("Licensor"), and
- **[LICENSEE LEGAL NAME]**, a **[STATE/COUNTRY] [ENTITY TYPE]** located at **[LICENSEE ADDRESS]** ("Licensee").

Stripe payment reference: **[STRIPE INVOICE OR SUBSCRIPTION ID]**

## 1. Definitions

- **"ztd"** means the software published by Licensor at https://github.com/zerotrustdata/ztd. It includes the
  Gateway, Operator, protocol contracts and generated bindings, console, and associated tooling. It also includes
  every version Licensor releases during the Term, in source and binary form.
- **"Licensed Product"** means the product or service Licensee offers to its customers, described in Exhibit A.
  It incorporates ztd and provides material functionality beyond ztd itself.
- **"End Customer"** means a third party that receives or uses a Licensed Product from Licensee or a Distributor.
- **"Distributor"** means a reseller, channel partner, or systems integrator that Licensee authorizes to
  distribute a Licensed Product.
- **"Affiliate"** means an entity that controls, is controlled by, or is under common control with Licensee. Here,
  "control" means owning more than 50% of the voting interests.
- **"Term"** has the meaning given in Section 7.

## 2. License grant

During the Term, Licensor grants Licensee and its Affiliates a worldwide, non-exclusive, non-transferable license,
except as Section 11.2 allows, to:

1. use, copy, modify, and create derivative works of ztd to build, test, and operate Licensed Products;
2. run ztd, including in production, as part of Licensed Products, including Licensed Products that Licensee hosts
   and operates as a managed or software-as-a-service offering;
3. distribute ztd in binary form, unmodified or modified, as part of Licensed Products, directly or through
   Distributors, in unlimited quantities; and
4. remove or replace ztd product names, logos, and user-interface branding in Licensed Products ("white-label").

This license replaces the Business Source License 1.1 for all use within this Section. Licensee does not need to
display the Business Source License in Licensed Products.

## 3. Restrictions

Licensee may not:

1. offer, sell, or distribute ztd on its own, or as a product whose primary value is ztd's governance functions,
   in competition with ztd;
2. distribute ztd source code to anyone other than its Affiliates, contractors working on Licensed Products under
   written confidentiality obligations, or End Customers when a government or regulated contract requires source
   delivery;
3. sublicense ztd except as Section 4 allows; or
4. use Licensor's names or logos in a way that implies Licensor endorses a Licensed Product, unless Licensor
   agrees in writing.

## 4. End Customers and Distributors

1. Licensee may grant End Customers the right to use ztd as part of a Licensed Product under terms at least as
   protective of ztd as this Agreement. Licensee's own customer agreement is enough to do this.
2. Distributors may distribute Licensed Products only under a written agreement with Licensee that imposes those
   same protections.
3. Licensee is responsible for its Affiliates', contractors', and Distributors' compliance with this Agreement.

## 5. Third-party components

ztd includes open-source components under their own licenses, listed in `THIRD_PARTY_NOTICES.md` in the ztd
repository. This Agreement does not change those licenses. Licensee must comply with them, including any notice
requirements, when it distributes ztd.

## 6. Fees

1. The annual fee is **US $2,950**, paid through Stripe at checkout and on each renewal.
2. Fees exclude taxes. Licensee pays all taxes on the fees except taxes on Licensor's income.
3. Fees are non-refundable, except as stated in Licensor's Commercial License and Services Terms at
   https://zerotrustdata.io/terms/ in effect on the Effective Date.

## 7. Term, renewal, and termination

1. **Term.** This Agreement starts on the Effective Date and runs for one year. It renews automatically for
   successive one-year periods while Licensee pays the annual fee. The initial period and all renewals together are
   the "Term".
2. **Non-renewal.** Either party may choose not to renew by giving notice before the end of the current period.
   Licensee may do so by canceling its Stripe subscription.
3. **Termination for breach.** Either party may terminate this Agreement if the other party materially breaches it
   and does not cure the breach within 30 days after written notice.
4. **Effect of expiration or termination.**
   1. Licensee must stop distributing ztd in new copies of Licensed Products and stop using ztd in new
      deployments.
   2. Licensee may keep operating and supporting Licensed Products already deployed for existing End Customers.
      Those End Customers keep the right to use the copies of ztd they already received, without time limit.
   3. Licensee may continue internal production use of the last version of ztd it received during the Term for 90
      days, so it can migrate.
   4. Each version of ztd still converts to the Apache License 2.0 on its Change Date under the Business Source
      License 1.1, and Licensee keeps that right.
5. **Survival.** Sections 4.3, 5, 7.4, 7.5, and 9 through 11 survive expiration or termination.

## 8. Updates and support

1. During the Term, Licensee may use every ztd release Licensor publishes under this Agreement.
2. Licensor provides asynchronous email support to Licensee's engineering team and aims to give a written
   response within one business day. Licensor offers no uptime, response-time, or resolution service level.
3. Licensor does not support End Customers directly. Licensee supports its End Customers.

## 9. Ownership

1. Licensor owns ztd and all intellectual property rights in it. Licensee's modifications to ztd are derivative
   works that Licensee may use only under this Agreement.
2. Licensee owns the Licensed Products, excluding ztd.
3. If Licensee gives Licensor feedback or contributes code, Licensor may use it without obligation. Code
   contributions to the ztd repository follow that repository's contribution terms.

## 10. Warranties and liability

1. **Authority.** Each party represents that it has the authority to enter into this Agreement.
2. **Disclaimer.** Except for Section 10.1, ztd and all support are provided **as is**, without warranties of any
   kind, express or implied. This includes merchantability, fitness for a particular purpose, and
   non-infringement. Licensor does not warrant that ztd or any Licensed Product meets any regulatory, compliance,
   or certification requirement.
3. **Exclusion of damages.** Neither party is liable for indirect, incidental, special, consequential, or punitive
   damages, or for lost profits, revenue, or data, even if it was advised of the possibility.
4. **Cap.** Each party's total liability under this Agreement is limited to the fees Licensee paid in the 12 months
   before the claim. The cap does not apply to Licensee's breach of Section 3 or to use of ztd outside the license
   in Section 2.

## 11. General

1. **Export.** ztd includes encryption. Each party will comply with the U.S. export control and sanctions laws
   that apply to its own distribution of ztd. Licensee is responsible for export classification and compliance for
   Licensed Products.
2. **Assignment.** Neither party may assign this Agreement without the other's written consent. Either party may
   assign it, without consent and on notice, to a successor in a merger, acquisition, or sale of substantially all
   of the business or assets this Agreement relates to.
3. **Government end users.** ztd is "commercial computer software" provided to government end users only with the
   rights set out in this Agreement.
4. **Governing law.** Oregon law governs this Agreement, without regard to its conflict-of-laws rules. The state
   and federal courts in Multnomah County, Oregon, have exclusive jurisdiction.
5. **Notices.** Notices must be in writing and sent by email to the addresses below. Notice takes effect when the
   recipient acknowledges receipt or one business day after sending, whichever comes first.
6. **Entire agreement.** This Agreement, including Exhibit A, is the entire agreement about its subject matter and
   overrides Licensor's Commercial License and Services Terms where they conflict. Any amendment must be in writing
   and signed by both parties. Purchase order terms have no effect.
7. **Counterparts.** The parties may sign this Agreement electronically and in counterparts.

## Signatures

| | Licensor | Licensee |
| --- | --- | --- |
| Entity | Lateralus Labs, LLC | [LICENSEE LEGAL NAME] |
| Signature | | |
| Name | Danny Barbour | [SIGNER NAME] |
| Title | [TITLE] | [SIGNER TITLE] |
| Date | | |
| Notice email | danny@zerotrustdata.io | [LICENSEE NOTICE EMAIL] |

## Exhibit A: Licensed Product

- **Product or service name:** [PRODUCT NAME]
- **Description:** [What the product does and how it uses ztd]
- **Delivery model:** [Appliance / on-premises software / SaaS or managed service / government deliverable]
- **Licensee technical contact:** [NAME, EMAIL]
