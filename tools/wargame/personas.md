# War-game personas and scenarios

Two simulated parents share one Home OS household. Each one talks to their own private
agent (a moltis instance with the Home OS MCP attached as that member). They play the same
week in parallel, so every scenario also tests what the *other* adult's agent can see.

Round 1 was played on Sun 2026-11-15, household timezone `America/New_York`. Results:
project KB `discoveries/family-hub-war-game-2026-11-15.md`.

## The household (shared by both personas)

- Two adults, both household owners: **Jacob** and **Amber**.
- Three kids. Refer to them as **Kid A (9, piano Mondays)**, **Kid B (6, soccer Tue/Thu)**
  and **Kid C (3, preschool M/W/F mornings)**. Pick any first names you like for a round;
  don't commit real ones.
- One house, two cars (a minivan with an oil change due at 45k miles or Dec 1, and a second
  car), one dog.
- Hosting Thanksgiving: grandparents arrive Wed 11/25 around 4pm and stay through Sunday;
  dinner Thursday at 3pm.

## How to play a persona

- Talk the way a busy parent texts: short, relative dates ("next Wednesday", "two weeks from
  Tuesday"), several asks in one message, follow-ups like "did that go through?".
- Never tell the agent which tool to use. If it asks a clarifying question, answer it like
  the parent would.
- One topic per session (`chat.sh <persona> <topic> ...`). Open a new session when the
  topic changes, the way a real person starts a new chat.
- After every turn that should have written something, check it with a ground-truth MCP
  call (`mcpcall.py`) as that member, and as the other member when it is meant to be shared.
- Score each scenario PASS / PARTIAL / FAIL, and record the evidence: tool calls, the
  ground-truth result, the reply, and how long it took.

## Persona: Jacob

Handles setup, money, the house and the cars.

| # | Scenario | Sample turns |
|---|---|---|
| 1 | Onboarding | "Here's our family: three kids (ages), the house, both cars, the dog. Put it in home-os so Amber sees it." Then: "Is that in home-os? Amber needs to see it." |
| 2 | Calendar + relative dates | "Kid B soccer every Tue/Thu 5:30-6:30 through mid Dec, family calendar." "Dentist next Wednesday 3:40; preschool party Dec 18 10am; date night a week from Saturday 7pm." |
| 3 | Conflicts and free time | "Book Kid B a haircut this Thursday at 5:45." "What free 1-hour slots do we have Thursday 2-8pm?" |
| 4 | Bills | "Add mortgage $2,340 due the 1st, electric ~$180 the 18th, internet $75 the 22nd." "Mark electric paid." "What's due in the next two weeks, and what's our monthly fixed spend?" |
| 5 | House and car maintenance | "HVAC filter every 90 days, last changed Sept 1." "Minivan oil change at 45k miles or Dec 1, whichever comes first." |
| 6 | Notes and documents | "Save Kid A's teacher, room number and pickup rules." Paste a receipt and say "file this". |
| 7 | Daily briefing | "What do I need to know for tomorrow? And the rest of this week?" |
| 8 | Cross-member visibility | "Thanksgiving grocery run Sat the 21st 9am, I'm taking the kids" (for Amber to see). "What has Amber added to home-os this week?" |
| 9 | Edge cases | "Dentist moved to 4:20 same day; cancel date night on the 28th." "Start a Thanksgiving grocery list and a weekly chore chart; remind me 30 min before soccer Tuesday." "Is date night still on for the 28th?" |

## Persona: Amber

Handles the kids' schedules, health, Thanksgiving and lists.

| # | Scenario | Sample turns |
|---|---|---|
| 1 | Kid logistics | "Put the kids' stuff on the family calendar: Kid C preschool M/W/F 8:30-11:30, Kid A piano Mon 4:15 (45 min), school out Wed-Fri Thanksgiving week." "Preschool is closed when school is out, and winter break is 12/21-1/1, take those off. Kid B field trip Fri Dec 4, $12 check + slip by Dec 1." "Remind me the Tuesday before to write the check." |
| 2 | Health | "Book Kid C's well visit two weeks from Tuesday at 9:15; flu shots the first week of Dec when nobody has anything." "Pediatrician is <name, practice, address>, save that so we have it." |
| 3 | Thanksgiving | "Parents in Wed 25th ~4, stay through Sunday; dinner Thu at 3. What does Thanksgiving week look like?" |
| 4 | Coordination with Jacob | "Jacob picks up the kids Tue 11/17, I have a late meeting, put that on so he sees it." "What has Jacob put on the calendar? Is he free Thu 11/19 evening?" |
| 5 | Budget | "How much do we spend on bills each month? What's due before the end of the month?" "Jacob said he put electric, internet and mortgage on there. Can we afford $600/kid for Christmas?" |
| 6 | Notes and documents | "Put the pediatrician's info in home-os so Jacob has it; kids' clothing sizes; start a Christmas gift idea list." "Got this bill in the mail, file it: <practice>, statement 11/10, copay $35.00 due 12/10. Make sure we don't forget to pay it." |
| 7 | Lists and reminders | "Start a Thanksgiving grocery list and a weekly chore chart; Jacob should be able to add to the grocery list too." "Add whipped cream and a bag of ice; check off the turkey, Jacob already ordered it." |
| 8 | Daily briefing | "What do I need to know for tomorrow?" "And what's this week look like?" |
| 9 | Edge cases | "Move Kid A's piano next Monday to 5pm." "Cancel the flu shots on the 5th, we'll walk into CVS. Also, when is Kid B's dentist appointment?" (never entered: the agent must not invent one) |

## Agent system-prompt snippet

Round 1 gave both agents this guidance about the hub. Keep it (or its successor) in the
agent's system prompt so household facts go to Home OS, not private memory.

```markdown
## Household hub (home-os) — `mcp__home-os__*`

home-os is the family's shared system of record. Everything you put there is
visible to the other adults in the household and their agents.

- **Calendar** — `list_calendars`, `list_events`, `create_event`, `update_event`,
  `delete_event`, `check_conflicts`, `find_available_slots`. Check conflicts
  before booking; put family events on the shared family calendar.
- **Today / this week** — `get_daily_briefing` first when asked "what's going on".
- **Money & bills** — `list_bills`, `get_upcoming_bills`, `get_bill_summary`,
  `get_financial_summary`, loans.
- **Notes** — `create_note`, `list_notes` (attach to an entity when relevant).
- **House stuff** — properties, assets, vehicles, pets, vendors, maintenance.
- **Documents** — `upload_file`, `list_files`, `search_files`, `search`.
- Always resolve relative dates ("next Tuesday") against today's date and the
  household timezone before creating anything; state the exact date back.
- If home-os has no tool for what's needed, say so plainly — don't fake it in
  a note unless the user agrees.
- Never claim a reminder or shared record exists unless you created it.
```
