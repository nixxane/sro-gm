# sro-gm

Create a ready-to-play **GM test character** on a local [OpenSRO](https://github.com/opensro-dev/opensro) world in one command: any level, every mastery and skill learned, unlimited SP, GM granted.

```powershell
.\sro-gm.ps1 -OpenSRO C:\opensro -Restart -account gm -name "[GM]Test"
```

That stops the server, creates account `gm` with a generated password, creates `[GM]Test` (European, level 90, all 6 masteries at 90, every skill at its top rank, 2,000,000,000 SP), grants GM, and redeploys. Log in at your client (http://127.0.0.1:5180 by default). The password is in `apps/server/.state/sro-gm-logins/gm.txt`.

## Requirements

A working local OpenSRO setup from its [Getting started](https://github.com/opensro-dev/opensro/blob/main/docs/GETTING_STARTED.md) guide: Go, a built server game-data tree (`pnpm task build server-game-data`), and a world created by `sro-bootstrap-development`.

## Usage

Windows (PowerShell):

```powershell
.\sro-gm.ps1 -OpenSRO <path to opensro> [-Restart] <options>
```

Linux, macOS or Git Bash:

```bash
./sro-gm.sh --opensro <path to opensro> [--restart] <options>
```

`-Restart` / `--restart` stops the fleet first and redeploys afterwards (`sro-nomad stop`, then `sro-nomad deploy -build`), so the GM grant is live when it finishes. Without it, stop the GameWorld yourself before running and redeploy after: the tool needs the world database to itself.

| Option | Default | Meaning |
| --- | --- | --- |
| `-account` | required | Login account. Created when missing; an existing account is used as is. |
| `-name` | required | Character name, e.g. `[GM]Test`. |
| `-password` | generated | Password for a **new** account (8+ characters). Generated passwords are saved to `.state/sro-gm-logins/<account>.txt` and never printed. |
| `-race` | `eu` | `eu` or `ch`. |
| `-gender` | `male` | `male` or `female`. |
| `-model` | race default | Model codename, e.g. `CHAR_EU_WOMAN_WITCH`. |
| `-level` | `90` | 1 to 140. Stats follow the native formula (20 + level - 1 STR/INT, 3 free points per level). |
| `-masteries` | `all` | `all`: every racial mastery at the character level. `none`: masteries at 0. |
| `-skills` | `all` | `all`: every racial skill group at the highest rank the character can learn. `base`: creation skills only. |
| `-sp` | `2000000000` | Skill points. |
| `-gm` | `true` | Add the character to `gm-characters.txt`. `-gm=false` makes a normal test character. |
| `-division` | `global-official` | Shard division. |
| `-state` | `.state` | Server state folder (`apps/server/.state`). |

Examples:

```powershell
# Chinese female GM at level 80
.\sro-gm.ps1 -OpenSRO C:\opensro -Restart -account gmch -name "[GM]Mei" -race ch -gender female -level 80

# A second character on an account that already exists, not GM
.\sro-gm.ps1 -OpenSRO C:\opensro -Restart -account gm -name "Alt90" -gm=false
```

## How it works

Go lets only code inside the OpenSRO server module import its internal packages. So the launcher copies `sro-gm/main.go` into the checkout's git-ignored `apps/server/.state/sro-gm/` and runs it from there. Nothing tracked in your checkout changes.

The tool writes through the server's own authority store (the same calls the server uses to create and rename characters), never by editing database rows directly:

1. It snapshots the world database to `.state/backups/before-sro-gm-<time>.sqlite`.
2. It creates the character with a valid creation loadout, then renames it offline to your name, so names like `[GM]...` work.
3. It sets level, stats, masteries and SP, and stores skills the way the server does when you level them: one entry per skill group at its top rank. A rank is only included when the character meets its mastery, STR/INT, race and prerequisite requirements, so the skill list is one the server accepts.
4. It adds the account to `cluster/accounts.json` (bcrypt) and the character to `cluster/gm-characters.txt`.
5. It reopens the database and verifies what was written.

To undo, stop the fleet, put the backup back as `shards/<division>/authority/state.db`, and remove the account and GM entries.

## Notes

- For local and test worlds. A GM character with every mastery at its level is above what a native European character could hold; that is the point of a test character.
- Tested against OpenSRO `main` as of October 2026. The tool uses the server's internal packages, so a future OpenSRO change can break the build; the error will name what moved.

## License

MIT
