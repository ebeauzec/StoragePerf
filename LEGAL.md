# Legal Notice & Intellectual Property Declaration

> This document accompanies the [LICENSE](LICENSE) and
> [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) files and provides the full
> legal and intellectual property context for **Plumb** (repository name:
> `StoragePerf`). These three documents together constitute the complete terms
> governing this Software.

---

## 1. Ownership, Intellectual Property Rights and Independent Development

This software application, including without limitation its source code, object
code, documentation, technical specifications, architecture, designs, workflows,
configurations, prompts, scripts, build materials, databases, user interfaces,
and all related materials, content and developments, whether existing now or
created in the future, is the sole and exclusive intellectual property of
**Obi1 - FZCO**, EXCEPT for the third-party open-source components listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md), which remain the property of
their respective copyright holders under their own licenses.

All rights, title and interest in and to the Author's original portions of the
software, including all copyright, economic rights, moral rights to the extent
applicable, neighbouring rights, database rights, know-how, trade secrets,
inventions, improvements, derivative works, updates, enhancements and all other
intellectual property rights, are and shall remain exclusively vested in
**Obi1 - FZCO**, unless expressly transferred by it under a separate written
agreement signed by it.

The software was independently conceived, authored, developed, tested and
assembled by **Obi1 - FZCO** on its own time and using independent tools,
resources and development environments. The software was not created as a
work-for-hire, commissioned work, employment deliverable, client assignment,
internal project, sponsored project, or contractual obligation for any employer,
former employer, client, sponsor, platform provider, user, contributor or third
party — including Pure Storage, Inc. or NetApp, Inc.

No employer, former employer, client, sponsor, platform provider, user,
contributor or third party shall acquire any ownership interest, licence,
royalty, profit-share, assignment right, benefit, claim, control, or other right
in or to the software by reason of Obi1 - FZCO's past or present engagements,
sponsorship, administrative status, professional relationship, access to the
software, use of the software, feedback, contribution, or use of independent
development tools.

Any use of third-party tools, including generative-AI assisted development
tools, was carried out solely as an independent development aid under Obi1 -
FZCO's direction, review, testing, selection and control. No confidential,
proprietary, customer, internal, employer-owned, client-owned, or trade-secret
information of any employer, former employer, client, sponsor, platform provider,
user, contributor or third party was submitted to, uploaded into, disclosed to,
or used with such tools in connection with the development of the software.

All rights not expressly granted in writing by Obi1 - FZCO are strictly
reserved. No person or entity may copy, reproduce, modify, adapt, translate,
publish, distribute, commercialise, sublicense, sell, assign, transfer, pledge,
reverse engineer, remove attribution from, or claim authorship or ownership of
the Author's original portions of the software, except as expressly authorised
in writing by Obi1 - FZCO, and except as separately permitted for the
third-party open-source components under their own licenses.

If any third-party proprietary material is credibly identified as having been
inadvertently included in the software, Obi1 - FZCO reserves the right to
remove, replace or remediate such material promptly, without admission of
liability and without prejudice to its ownership of the remaining software.

Copyright © 2026 Obi1 - FZCO. All Rights Reserved.

---

## 2. Third-Party Product References & Trademarks

Any references to third-party products, services, companies, platforms,
trademarks, technologies or tools are made solely for identification,
compatibility, interoperability, technical, or documentation purposes. Such
references do not imply any affiliation, sponsorship, endorsement, approval,
authorisation, partnership, licence, or commercial relationship with the relevant
third-party owner. All third-party trademarks, product names, company names and
service names remain the property of their respective owners.

Specifically:

- **Pure Storage®, FlashArray®, FlashBlade®, and Purity®** are trademarks or
  registered trademarks of Pure Storage, Inc. Pure Storage, Inc. is not a
  sponsor, contributor, owner, or licensor of this Software.
- **NetApp®, ONTAP®, StorageGRID®, and Active IQ®** are trademarks or
  registered trademarks of NetApp, Inc. NetApp Harvest is an open-source
  project published by NetApp under the Apache License 2.0; this Software's
  independent ONTAP/StorageGRID collector was written by consulting Harvest's
  publicly published source code and Grafana dashboard definitions to confirm
  metric names and semantics (see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md))
  — Harvest's binary is not bundled with or executed by this Software. This
  reference use does not imply NetApp's endorsement, certification, or
  support of this Software.

No customer names, customer-specific data, internal pricing, internal
operational procedures, or confidential business information belonging to
either vendor is embedded in, distributed with, or derivable from this
Software.

---

## 3. No Proprietary Third-Party Information

The Author's original code in this Software is built exclusively on:

- **Publicly documented, vendor-published metrics interfaces** — Pure
  Storage's native FlashArray/FlashBlade OpenMetrics endpoints and NetApp's
  Harvest and StorageGRID Prometheus metric schemas, all of which those
  vendors publish specifically so third-party tools can consume them;
- **Public, vendor-maintained open-source reference projects** that document
  those interfaces, named individually in
  [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md);
- **Public product documentation** (docs.netapp.com, NetApp's public GitHub
  organizations, Pure Storage's public GitHub organization);
- **The Author's independent analysis** of that publicly available information.

Using a vendor's own published metric names and label conventions to
interoperate with the monitoring interface that vendor built for exactly that
purpose is standard, expected practice — the same category of activity as
writing an HTTP client against a published REST API. No metric name, field, or
interface identifier used in this Software's threshold configuration was
obtained from any non-public source.

The Software does not contain and was not built using any:

- Internal systems, codebases, or internal tooling of any employer, client,
  Pure Storage, Inc., or NetApp, Inc.;
- Confidential product roadmaps, pricing, or strategies;
- Customer-specific data obtained through employment or professional access;
- Non-public API specifications or internal API documentation.

---

## 4. Third-Party Open-Source Components

This Software downloads, bundles, and/or depends on third-party open-source
software to function — most significantly, official pre-built binary releases
of **Prometheus** and **VictoriaMetrics**, each obtained directly from that
project's own official release channel and used unmodified. None of the
Author's proprietary license terms in [LICENSE](LICENSE) apply to these
components — each remains governed exclusively by its own original license.
(NetApp Harvest is not bundled — see Section 2 above for how its published
source was used purely as a reference, not as bundled code.)

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the complete,
itemized list of every such component, its copyright holder, its license, and
where the full license text is reproduced (under `LICENSES/`). That file, and
the license texts it references, are included in every distributed copy of
this Software's compiled packages, as those components' own licenses require.

---

## 5. License Summary

| Use Case | Permission |
|---|---|
| Personal / educational / research use | ✅ Free, no approval required |
| Non-commercial organisational use (internal, no revenue) | ✅ Free, no approval required |
| Sharing with colleagues (same org, non-commercial, no charge) | ✅ Permitted |
| Modification for personal non-commercial use | ✅ Permitted |
| Commercial use (any revenue-generating or cost-saving context) | ⛔ Requires Author's **prior written consent** |
| Redistribution of the Author's original code (source or compiled) | ⛔ Requires Author's **prior written consent** |
| Redistribution of the third-party open-source components | ✅ Permitted under each component's own license — see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) |
| Derivative works distributed to third parties | ⛔ Requires Author's **prior written consent** |
| Claiming authorship or ownership | ⛔ Prohibited |
| Removing copyright / attribution notices | ⛔ Prohibited |
| Representing as an official Pure Storage or NetApp product | ⛔ Prohibited |

See [LICENSE](LICENSE) for the full, binding terms governing the Author's
original code, and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the
terms governing bundled third-party components.

---

## 6. Attribution Requirement

Any permitted use, deployment, or sharing of this Software must retain:

1. This `LEGAL.md` file, intact and unmodified;
2. The `LICENSE` file, intact and unmodified;
3. The `THIRD_PARTY_NOTICES.md` file and the `LICENSES/` directory, intact and
   unmodified;
4. The copyright notice `Copyright © 2026 Obi1 - FZCO` wherever the
   Software or its output is displayed or distributed.

Removing, obscuring, or modifying attribution is a breach of the License and
may constitute an infringement of the Author's moral rights, or, for the
third-party components, a breach of those components' own license terms.

---

## 7. Commercial Licensing & Contact

If you wish to use this Software in a commercial context, integrate it into a
product or service, offer it as part of a managed service, or distribute the
Author's original code to third parties, you must obtain a commercial license
from the Author **before** any such use commences.

Retroactive licensing is not available. Commencing commercial use without
written authorisation constitutes infringement of the Author's intellectual
property rights and may result in legal action.

To request a commercial license or discuss authorised use, contact the Author
through the repository's issue tracker or via the contact information published
in the project documentation.

---

## 8. No Warranty / Limitation of Liability

This Software is provided for informational and operational support purposes
only. **The Author makes no warranty** — express or implied — as to the
accuracy, completeness, or fitness for purpose of any data, threshold,
finding, or report displayed by this Software, including the illustrative
performance thresholds shipped in `config/thresholds/*.yml`, which are
starting points for tuning, not vendor-certified values.

Users are solely responsible for independently verifying all findings and
recommendations before acting on them against production systems.

The Author shall not be liable for any loss, damage, outage, data corruption,
or financial harm arising from reliance on this Software's output.

---

## 9. Governing Provisions

- This `LEGAL.md`, the accompanying `LICENSE` file, and
  `THIRD_PARTY_NOTICES.md` constitute the complete legal terms governing this
  Software.
- In the event of any conflict between this document and the `LICENSE` file
  regarding the Author's original code, the `LICENSE` file prevails. In the
  event of any conflict regarding a third-party component, that component's
  own license (reproduced under `LICENSES/`) prevails.
- If any provision is found unenforceable, the remaining provisions continue
  in full force.
- Failure by the Author to enforce any provision does not constitute a waiver
  of future enforcement rights.

---

*Last updated: 2026-08-27*
*Copyright © 2026 Obi1 - FZCO. All Rights Reserved.*
