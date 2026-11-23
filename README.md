# MiniCRM

MiniCRM is a small app for working your book of business. It runs on your own computer and opens in your web browser. It sorts your accounts into priority groups ("buckets") by renewal date, ARR and AutoRenew, and lets you keep a risk flag, a cadence, notes and contract updates on each account.

Your data stays on your computer. Nothing is sent anywhere.

## Get it and start it
### OPTION 1: Create a macOS app bundle (.app)

If you'd rather double-click a normal Mac app icon instead of running from
Terminal, you can package the binary into a `.app` bundle:

Download this repository, open Terminal in the repository folder and run
the following commands:

```sh
./make-mac-app.sh              # Apple Silicon (default), version 1.0.0
./make-mac-app.sh amd64        # Intel Mac
./make-mac-app.sh arm64 1.0.0  # Custom architecture and version
```

This creates `MiniCRM.app`. Double-click it, then open
<http://127.0.0.1:4300> in your browser.


### OPTION 2: Download a pre-compiled binary
Download the file for your computer from the [latest release](https://github.com/jplassnibatt/minicrm/releases/latest):

| Your computer | File to download |
| --- | --- |
| Mac with an Apple chip (M1, M2, M3…) | `minicrm-darwin-arm64` |
| Mac with an Intel processor | `minicrm-darwin-amd64` |

**Give it a home.** Make a folder called `MiniCRM` in your home folder and move the downloaded file into it. On a Mac, MiniCRM keeps your data in `~/Library/Application Support/minicrm/` (the standard macOS location for app data).

**Start it (Mac).** Open the Terminal app and run these three lines, one at a time:

```bash
cd ~/MiniCRM
chmod +x minicrm-darwin-arm64                       # use your file name if it differs
xattr -d com.apple.quarantine minicrm-darwin-arm64  # remove the quarantine flag
./minicrm-darwin-arm64                              # run the app
```

You only need the `chmod` and `xattr` lines the first time. When it is running, Terminal shows a line that starts with `MiniCRM … running at http://127.0.0.1:4300`.

**Open it.** In your browser, go to <http://127.0.0.1:4300>. Bookmark it.

**Keep the Terminal window open** while you use MiniCRM. Closing it stops the app. To stop it on purpose, click that window and press `Ctrl` + `C`. Your data is saved as you go, so stopping loses nothing.

### macOS: first launch warning

If  `xattr -d com.apple.quarantine minicrm-darwin-arm64` is not run, Mac may say the file can't be opened or is from an unidentified developer (files downloaded from the internet). Open **System Settings → Privacy & Security**, scroll down to the message about `minicrm-darwin-arm64`, click **Open Anyway**, and start it again.

## The first time you open it

A new MiniCRM is empty: no accounts and no buckets of your own.

1. **Add the buckets.** On the **Buckets** page, click **Add the standard six**. This adds the usual priority groups (see [Buckets](#buckets)). You can change them later.
2. **Bring in your accounts.** There is no "new account" button. Accounts arrive in one of two ways:
   - **From a file.** Ask a teammate for their export file, then click **Import data** at the bottom of the left menu and pick it. See [Back up, move or share your data](#back-up-move-or-share-your-data).
   - **Through AI or a script.** If your team has set that up, it adds and refreshes accounts from Salesforce for you. See the [technical reference](docs/REFERENCE.md).

## Finding your way around

The menu on the left has six pages:

| Page | What it shows |
| --- | --- |
| **Buckets** | Your active accounts sorted into priority groups |
| **Book of business** | Every account, including inactive ones |
| **No autorenew** | Active accounts with AutoRenew off that renew in under 90 days |
| **Notes log** | Every note you have saved, newest first |
| **Activity** | A history of every change made in the app |
| **API** | For scripts and tools. You can ignore it |

On the account pages:

- **Search** (top right) finds accounts by name, account owner or renewal manager.
- **Filters** narrow the list by Account Owner, Renewal manager, AutoRenew, Fiscal quarter and Risk flag.
- **Active / Inactive / All** switches which accounts are listed.
- **Column headings** sort the list when you click them.
- **Click an account** to open its panel on the right.

**Days to renewal** are always counted from today, so the numbers and buckets move forward on their own each day.

**Fiscal quarters** start in February: Q1 is Feb–Apr, Q2 May–Jul, Q3 Aug–Oct, Q4 Nov–Jan.

**Inactive** means one of three things: the account has $0 ARR and no renewal due in the next 30 days, you marked it Churned or New CSM, or it was dropped by a data load. Inactive accounts are hidden from Buckets. Find them under **Book of business → Inactive**.

## Working an account

Click an account to open its panel. It has four parts.

### Account details

Shows ARR, term end, AutoRenew, renewal status, account owner, renewal manager, Slack channel, EWS, last email and last Gong. Names shown in green with an underline are links: the account name opens Salesforce, people open Slack.

Click **Edit** to change them. Each box saves as soon as you click out of it; click **Done** when you finish. Three boxes take a link along with the text:

| Box | What to type or paste |
| --- | --- |
| **Account name** | `[Acme](https://…the Salesforce address…)` |
| **Slack channel** | `[#team-acme](https://…the channel address…)` |
| **Account Owner**, **Renewal manager** | The name in one box, the person's Slack address in the box next to it |

That `[text](address)` form is how you write a link: the text people see goes in square brackets, the address in round brackets. The easy way is to copy a link in Slack or Salesforce and paste it. MiniCRM fills in that form for you.

### Tracking

- **Current risk:** At risk, Not at risk, or Not set.
- **Cadence:** how often you meet the account.
- **Details:** one free-text box for standing information about the account. Web addresses in it become clickable. Click **Edit** to change it. Saving replaces the previous text.

Use Notes for "what happened today" and Details for "what is always true about this account".

### Notes

Write a note and click **Add note**. Each note is saved with its date and time, and you can keep as many as you like. **Delete** removes one. Notes never overwrite each other.

### Sync and change tracking

All account data flows through AI: ask AI to update the account, it curls `PUT /api/accounts/<id>` (or `PUT /api/accounts/<id>/contract`) with the right fields, and MiniCRM stores it. The UI contract section below is a manual fallback — you don't need it if you go through AI.

MiniCRM records two timestamps on every account:

| Field | Meaning |
| --- | --- |
| **claudeUpdatedAt** | When AI last synced this account (API calls with header `X-MiniCRM-Source: claude-code`) |
| **manualUpdatedAt** | When renewal fields (ARR, term end, AutoRenew, renewal status, contract outcome) were last changed by anyone other than AI — including manual edits in this UI section |

They show in the account drawer under **Sync info**. If `manualUpdatedAt` is newer than `claudeUpdatedAt`, a renewal field was changed manually after AI's last sync — check the **Activity** page for exactly what changed.

A comparison line shows who is ahead — for example "AI ahead of human by 3 days" or "Human ahead of AI by 2 weeks" — and the older timestamp is shown in red.

#### Update Contract Info (manual fallback)

Use this when a renewal decision has been made and you're not going through AI. Choose an **Outcome** and click **Apply**:

| Outcome | What happens |
| --- | --- |
| **Renewed** | Account stays Active, renewal status shows Green, and you enter the new ARR, term end and AutoRenew. The account moves to the bucket that fits the new values. |
| **Churned** | Account becomes Inactive. |
| **New CSM** | Account becomes Inactive (reassigned to a new CSM). |
| **No change** | Clears any previously recorded contract update. |

When **Renewed** is chosen, the **Last change on** date is recorded so you can see when the update was applied. A new data import overwrites these values. Manual edits here count as a manual renewal-field change and update `manualUpdatedAt`.

#### Via the API (curl)

```bash
# AI sync: stamps claudeUpdatedAt
curl -X PUT http://127.0.0.1:4300/api/accounts/1 \
  -H "Content-Type: application/json" \
  -H "X-MiniCRM-Source: claude-code" \
  -d '{"arr": 7000, "termEnd": "2031-01-31", "autorenew": true}'

# Manual change to a renewal field: stamps manualUpdatedAt
curl -X PUT http://127.0.0.1:4300/api/accounts/1 \
  -H "Content-Type: application/json" \
  -d '{"arr": 7500}'

# Record a contract outcome: stamps manualUpdatedAt (unless X-MiniCRM-Source: claude-code is sent)
curl -X PUT http://127.0.0.1:4300/api/accounts/1/contract \
  -H "Content-Type: application/json" \
  -d '{"outcome": "Renewed", "newArr": 7000, "newTermEnd": "2031-01-31", "newAutorenew": true}'
```

If an API key is set, add `-H "MiniCRM-X-Key: <your-key>"` (or `Authorization: Bearer <key>`).

See the [technical reference](docs/REFERENCE.md) for the full API.

## Buckets

A bucket is a group of accounts that share a rule, for example "AutoRenew off, more than $25k ARR, renewing in 30 to 60 days". MiniCRM checks your buckets from first to last and puts each account in the first one whose rule it fits. When an account's renewal date, ARR or AutoRenew changes, it moves to the right bucket by itself.

**The standard six** that the button adds:

| Bucket | AutoRenew | ARR | Renews in |
| --- | --- | --- | --- |
| First Priority Risk | Off | More than $25k | 30 to 60 days |
| Second Priority Risk | Off | Any | 59 days or less |
| Third Priority Risk | Off | More than $25k | 61 days or more |
| Fourth Priority Risk | Off | $25k or less | 151 days or more |
| First Priority Expansion | Any | More than $25k | 60 to 120 days |
| Second Priority Expansion | Any | More than $10k | 30 to 150 days |

Two more buckets are always there and can't be deleted:

- **Month-to-Month:** every account with no term end date.
- **Future Watch:** every account with a term end date that fits none of your buckets.

**To add or change a bucket,** open the **Buckets** page and click **Add bucket**, or open a bucket and click **Edit bucket**. Give it a name, a colour and its limits. Leave a limit empty to mean "no limit". The position decides which bucket is checked first.

## Back up, move or share your data

Both buttons are at the bottom of the left menu.

**Export data** saves one file, `minicrm-darwin-export-<date>.json`, to wherever your browser saves downloads (usually the Downloads folder). It holds every account, all notes, contract updates and your buckets.

**Import data** reads such a file. It asks you to confirm first, then:

- updates accounts that have the same name with the values in the file,
- adds accounts you don't have yet,
- adds notes without creating duplicates,
- adds buckets you don't have yet.

Import never deletes anything, and accounts that are not in the file are left alone.

Use them to:

- **Back up:** click Export data now and then, and keep the file somewhere safe.
- **Move to a new laptop:** Export on the old one, Import on the new one.
- **Share with a teammate:** send them your export file. They click Import data.

The export file contains customer names and ARR. Keep it inside the company.

## Updating MiniCRM

The bottom of the left menu shows your version and a **Check for updates** button. It tells you where you stand:

| The button says | Meaning | What to do |
| --- | --- | --- |
| **Update available (v1.2.0)** | A newer version exists | Click it and follow the steps below |
| **Up to date** | You have the latest | Nothing |
| **Ahead of latest version** | You have a newer test build | Nothing |
| **Check for updates** | It couldn't check | Click it to open the download page and compare yourself |

To update:

1. Stop MiniCRM (`Ctrl` + `C` in its window).
2. Download the new file for your computer, as in [Get it and start it](#get-it-and-start-it).
3. Put it in your `MiniCRM` folder in place of the old file.
4. Start it again.

Your data lives outside the app file (on a Mac, in `~/Library/Application Support/minicrm/`), so updating does not touch it. Clicking Export data before you update gives you a backup anyway.

MiniCRM never updates itself. Checking for updates only asks GitHub which version is the latest.

## If it asks for an API key

An API key is a password for MiniCRM. You only meet it if someone created one on the **API** page of your MiniCRM. From then on the app asks for it.

- **Your browser shows "This server requires an API key":** paste the key and click OK. The browser remembers it.
- **It asks again although you already entered it:** you probably opened a different address. `http://127.0.0.1:4300` and `http://localhost:4300` look alike, but the browser treats them as two sites and remembers the key separately for each. Use one address always, or enter the key once for the other.
- **You lost the key:** stop MiniCRM, then run `./minicrm-darwin-arm64 keys-reset` in Terminal from your `MiniCRM` folder (use your own file name) and start MiniCRM again. It removes every key, so the app stops asking.

If you never created a key, MiniCRM never asks for one.

## When something goes wrong

| What you see | What to do |
| --- | --- |
| The browser says the page can't be reached | MiniCRM isn't running. Start it again ([Get it and start it](#get-it-and-start-it)) and keep its window open |
| Terminal says `address already in use` | MiniCRM is already running in another window. Use that one, or close it first |
| An account is missing from Buckets | It is probably Inactive. Look under **Book of business → Inactive** |
| An account is in Month-to-Month and you expected another bucket | It has no term end date. Add one under **Edit** |
| A link shows as plain text | It needs the `[text](address)` form, and the address must start with `https://` |
| It asks for an API key | See [If it asks for an API key](#if-it-asks-for-an-api-key) |

## Where your data lives

Everything is in one file on your computer. On a Mac, MiniCRM keeps it in `~/Library/Application Support/minicrm/crm.db`

It is not shared with anyone and is not backed up for you. Use **Export data** for backups.

## Found a bug, or have an idea?

Click **Open an issue on GitHub** at the bottom of the left menu, or go to <https://github.com/jplassnibatt/minicrm/issues>. Say what you did, what you expected and what happened instead. Please don't paste customer names or ARR into an issue.
