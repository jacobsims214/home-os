# What the year corpus needs in the household before it can run

The year is played against a **seeded** household, not invented data — that is the rule
`how-to/wargame-scenario-corpus.md` sets ("use the seed's shapes, not invented data"). This
file is the precondition list: what the twelve months assume exists, and where each thing
comes from.

Nothing here is a scenario. A scenario's `steps` say what to do; this says what has to be
true before step one, or the scenario fails for the wrong reason (a missing vendor reads as
an agent that could not find one).

## Provided by `tools/wargame/seed/household.py` today

| The year assumes | Seed source | Used by |
|---|---|---|
| 2 parents (Jacob, Amber), 3 children (Maya 9, Noah 6, Lily 3) | `PEOPLE` | every `people` and `calendar` scenario |
| The house as one property with an address | `PROPERTY` — **18 Orchard Lane** | spring septic document, winter claim |
| Furnace as its own **asset** | `ASSETS` (`hvac`) | `jacob-spring-furnace-filter`, `jacob-fall-furnace-*` |
| 2 vehicles (Odyssey, Camry) | `VEHICLES` | `jacob-summer-vehicle-registration-renewal` |
| Biscuit the dog, and the vet vendor | `PETS`, `VENDORS` (`vet` = Warwick Animal Hospital) | `amber-summer-dog-vet-visit`, `-vaccination-record-filed` |
| A plumber vendor with a phone | `VENDORS` (`plumber` = Orchard Plumbing) | `amber-summer-plumbing-emergency` |
| 12 bills putting all four `billdue.AttentionFor` states on the board, including the homeowners-insurance bill (autopay with an account, due in 2 days) | `BILLS` | `amber-spring-tax-bill-reminder` — that autopay bill is its **control case** — and `jacob-fall-insurance-renewal` |
| A mortgage bill with a paying account | `BILLS` (`mortgage`) | the same control case |
| 4 `kb_docs`, one of them `how-to/money/insurance-claim` | `KB_DOCS` | `jacob-winter-burst-pipe-claim` |

**Children's names are Maya / Noah / Lily**, matching the seed. The ticket that scoped this
corpus named different children; the seed's placeholders win, because `tools/wargame/README.md`
forbids real kids' names and because a scenario naming a child the household lacks fails on its
first `person_find`.

## The seed does NOT provide, and the year needs

Each of these is a **setup precondition**, not an assertion. They belong in `household.py` (or
a `seed/year_setup.py` run once before March) so a replay starts from the same household every
time. Adding them to the seed is a separate change with its own revert-check; this is the list
that change has to cover.

| Needed | Why the year cannot run without it | First used |
|---|---|---|
| **A second property: the cabin** | `jacob-fall-cabin-winterization` asserts the checklist attaches to the cabin's own property id and not the house's. With one property that distinction cannot fail, so the scenario would pass vacuously. | November |
| A **tax authority / school district vendor** with a phone | The property-tax scenario's reachability half needs a payee that has a number to compare a `tel:` href against. | March |
| A **fence contractor vendor** (Orchard Fence) | `amber-spring-fence-quote` asserts a `tel:` href equals `contact_find`'s phone. With no vendor there is no number, and the assertion is unjudgeable. | April |
| A **restoration company** for the burst-pipe claim | Same shape as the fence quote, in December. | December |
| The **2026 tax documents**, filed with a tax year | `jacob-winter-tax-gather-agent` counts what the house *has* against what the agent claims is missing. An empty tax folder makes the count true for the wrong reason. | January |
| The **power-outage KB doc** | `amber-winter-power-outage-guide` and `-kb` assert the guide exists and states a specific number of hours. The seed's four KB docs do not include one. | January |
| The **$19.99 subscription bill** | `jacob-winter-subscription-found` and `-cancelled` turn on finding and ending a recurring charge; the cancellation's parity is that obligations drop by exactly 19.99. | February |

## Three rules this list exists to enforce

1. **A scenario that needs something the household lacks fails as a FALSE FAIL.** A false fail
   is worse than no scenario: the next round stops reading the scorecard. Every item above is
   either in the seed or listed here as missing, so a red verdict can be attributed to the
   product or to the household — never to "nobody knows".
2. **Two scenarios use the free-text payee on purpose.** `Riverside Pediatrics (billing:
   555-0134)` is the *control case* for "who do I call": a payee with a number in its name and
   no vendor record. Do not "fix" that by giving it a vendor — the failure it guards is a payee
   card that invents a link.
3. **Never seed by hand before a round without recording it.** A household mutated by hand is
   not reproducible, and the next month's document-count assertion then counts a different set
   than the month before did.
