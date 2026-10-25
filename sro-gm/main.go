/*
===========================================================================

main.go - sro-gm: create a ready-to-play GM test character on a local
OpenSRO world in one command

The launcher (sro-gm.ps1 / sro-gm.sh) copies this file into the OpenSRO
checkout's git-ignored apps/server/.state/sro-gm/ and runs it from
apps/server, because Go lets only code inside the OpenSRO server module
import its internal packages. Writing through those packages (the
authority store's doors) is the point: the character is created, renamed
and verified exactly the way the server itself would, never by editing
SQLite rows behind its back.

It runs offline, with the GameWorld stopped, snapshots the world database
first, and re-reads what it wrote. It is a local test-character tool, not
a gameplay rule: masteries and skills may exceed what a native character
could hold.

Skills follow the server's learned-list model (progression
HandleSkillLearn): one root id per skill group, at its highest rank, never
a chain sub-row. A group is taken at the highest rank whose whole ladder
the character could learn: mastery levels, STR/INT, race, and the
prerequisite groups, resolved to a fixpoint.

Copyright (C) 2026 nixxane. This program is free software: you can
redistribute it and/or modify it
under the terms of the GNU General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version. It is distributed WITHOUT ANY WARRANTY; see the LICENSE
file for details.

===========================================================================
*/
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"opensro.online/server/internal/data/store"
	"opensro.online/server/internal/domain"
	"opensro.online/server/internal/game/enterworld"
	"opensro.online/server/internal/gamedata"
)

const (
	// The native level table has 140 rows; v1.150 players stop at 90.
	maxLevel = 140
	// Highest skilldata id scanned; the v1.150 table is far below it.
	maxSkillID = 200000
	// accountBcryptCost matches auth.MaxBcryptCost, the cost new accounts use.
	accountBcryptCost = 12
	passwordBytes     = 18
	minPasswordBytes  = 8
	// Native creation stats: 20 STR and INT, then +1 each and 3 free
	// points per level gained.
	baseStat          = 20
	statPointsPerLvl  = 3
	defaultSkillPoint = 2000000000
)

// config is every choice the command line makes.
type config struct {
	stateDir  string
	division  string
	account   string
	password  string
	name      string
	race      string
	gender    string
	model     string
	level     int64
	masteries string
	skills    string
	sp        int64
	gm        bool
}

/*
================
main
================
*/
func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err == nil {
		err = run(cfg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sro-gm:", err)
		os.Exit(1)
	}
}

/*
================
parseFlags
================
*/
func parseFlags(args []string) (config, error) {
	var cfg config
	fs := flag.NewFlagSet("sro-gm", flag.ContinueOnError)
	fs.StringVar(&cfg.stateDir, "state", ".state", "the server state directory (apps/server/.state)")
	fs.StringVar(&cfg.division, "division", "global-official", "shard division the character lives on")
	fs.StringVar(&cfg.account, "account", "", "login account id (created when missing)")
	fs.StringVar(&cfg.password, "password", "", "password for a NEW account (default: generated and saved to a file)")
	fs.StringVar(&cfg.name, "name", "", "character name, e.g. [GM]Test")
	fs.StringVar(&cfg.race, "race", "eu", "eu or ch")
	fs.StringVar(&cfg.gender, "gender", "male", "male or female")
	fs.StringVar(&cfg.model, "model", "", "character model codename (default: the race/gender's first creation model)")
	fs.Int64Var(&cfg.level, "level", 90, "character level, 1..140")
	fs.StringVar(&cfg.masteries, "masteries", "all", "all (every racial mastery at the character level) or none")
	fs.StringVar(&cfg.skills, "skills", "all", "all (every skill at its top learnable rank) or base (creation skills only)")
	fs.Int64Var(&cfg.sp, "sp", defaultSkillPoint, "skill points")
	fs.BoolVar(&cfg.gm, "gm", true, "grant GM through gm-characters.txt")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	switch {
	case cfg.account == "" || cfg.name == "":
		return cfg, errors.New("-account and -name are required")
	case cfg.race != "eu" && cfg.race != "ch":
		return cfg, errors.New("-race must be eu or ch")
	case cfg.gender != "male" && cfg.gender != "female":
		return cfg, errors.New("-gender must be male or female")
	case cfg.level < 1 || cfg.level > maxLevel:
		return cfg, fmt.Errorf("-level must be 1..%d", maxLevel)
	case cfg.masteries != "all" && cfg.masteries != "none":
		return cfg, errors.New("-masteries must be all or none")
	case cfg.skills != "all" && cfg.skills != "base":
		return cfg, errors.New("-skills must be all or base")
	case cfg.sp < 0:
		return cfg, errors.New("-sp must not be negative")
	case cfg.password != "" && len(cfg.password) < minPasswordBytes:
		return cfg, fmt.Errorf("-password must be at least %d bytes", minPasswordBytes)
	case strings.ContainsAny(cfg.name, ",:"):
		return cfg, errors.New("-name must not contain , or :")
	}
	if cfg.model == "" {
		cfg.model = defaultModel(cfg.race, cfg.gender)
	}
	return cfg, nil
}

/*
================
defaultModel

The first model each race and gender offers at creation.
================
*/
func defaultModel(race, gender string) string {
	models := map[string]string{
		"eu/male":   "CHAR_EU_MAN_NOBLE",
		"eu/female": "CHAR_EU_WOMAN_NOBLE",
		"ch/male":   "CHAR_CH_MAN_ADVENTURER",
		"ch/female": "CHAR_CH_WOMAN_ADVENTURER",
	}
	return models[race+"/"+gender]
}

/*
================
run
================
*/
func run(cfg config) error {
	paths, err := gamedata.Resolve()
	if err != nil {
		return fmt.Errorf("game data: %w (build it with `pnpm task build server-game-data`)", err)
	}
	roster, err := enterworld.LoadRoster(filepath.Join(paths.CharacterAuthorityDir, "catalog.json"))
	if err != nil {
		return err
	}
	catalogs, err := enterworld.LoadTextdataCatalogs(paths.TextdataDir)
	if err != nil {
		return err
	}
	raceKey := domain.RaceKeyEurope
	if cfg.race == "ch" {
		raceKey = domain.RaceKeyChina
	}
	var trained []uint32
	if cfg.skills == "all" {
		trained, err = topSkills(catalogs.Skills, raceKey, cfg.level, cfg.masteries == "all")
		if err != nil {
			return err
		}
	}

	dir := filepath.Join(cfg.stateDir, "shards", cfg.division, "authority")
	options := store.Options{
		RequireStore:     true,
		DefaultSkills:    enterworld.DefaultSkillSeeder(catalogs.Skills),
		DefaultInventory: enterworld.StarterInventorySeeder(roster, catalogs.Items, true),
	}
	s, err := store.Open(dir, options)
	if err != nil {
		return fmt.Errorf("open world %s: %w (is the GameWorld still running? stop it first, or use the launcher's restart option)", dir, err)
	}
	created, err := create(s, cfg, raceKey, roster, trained)
	s.Close()
	if err != nil {
		return err
	}
	// The character exists only after the store wrote it; the account and
	// GM list follow so a failed creation leaves neither behind.
	if err := ensureAccount(cfg); err != nil {
		return err
	}
	if cfg.gm {
		if err := grantGM(cfg); err != nil {
			return err
		}
	}
	if err := verify(dir, options, cfg, len(trained)); err != nil {
		return err
	}
	fmt.Println("backup:", created)
	if cfg.gm {
		fmt.Println("next: redeploy so the GameWorld applies the GM list (sro-nomad deploy)")
	}
	return nil
}

/*
================
create

Snapshots the world, creates the character under a temporary creation-safe
name, sets its progression, and renames it to the requested name. Returns
the backup path.
================
*/
func create(s *store.Store, cfg config, raceKey string, roster *enterworld.Roster, trained []uint32) (string, error) {
	for _, c := range s.Characters().CharactersForDivision(cfg.division) {
		if strings.EqualFold(c.Name, cfg.name) {
			return "", fmt.Errorf("%s already exists on %s; pick another -name", c.Name, cfg.division)
		}
	}
	backupDir := filepath.Join(cfg.stateDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	backup := filepath.Join(backupDir, "before-sro-gm-"+time.Now().UTC().Format("20060102T150405")+".sqlite")
	if err := s.SnapshotTo(backup); err != nil {
		return "", err
	}

	level, stat, points, sp := cfg.level, int64(baseStat)+cfg.level-1, int64(statPointsPerLvl)*(cfg.level-1), cfg.sp
	zero, one := int64(0), int64(1)
	gender := zero
	if cfg.gender == "female" {
		gender = one
	}
	// A valid creation loadout for each race: Europe a one-hand sword with
	// light armor, China a blade with its first protector.
	weapon, protector := int64(2), one
	if raceKey == domain.RaceKeyChina {
		weapon = one
	}
	temp, err := tempName()
	if err != nil {
		return "", err
	}
	c := &domain.Character{
		Name: temp, ModelCodename: cfg.model,
		Level: &level, MaxLevel: &level, Strength: &stat, Intellect: &stat,
		StatPoints: &points, SkillPoints: &sp,
		Gender: &gender, WeaponSelected: true, ArmorSelected: true,
		WeaponIndex: &weapon, ProtectorIndex: &protector,
		HeightIndex: &zero, VolumeIndex: &zero, BodyShapeByte: &zero,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if !enterworld.CharacterCreationValid(c, roster) {
		return "", fmt.Errorf("model %s is not a valid %s creation choice", cfg.model, cfg.race)
	}
	c.Masteries = domain.DefaultMasteries(raceKey)
	if cfg.masteries == "all" {
		for i := range c.Masteries {
			c.Masteries[i].Level = cfg.level
		}
	}
	if err := s.CreateCharacter(cfg.division, cfg.account, c); err != nil {
		return "", err
	}
	// Creation seeds the 0-SP racial base attacks; keep them and add the
	// trained ladder tops (each group appears once).
	if len(trained) > 0 && !s.UpdateCharacter(c, "sro-gm-skills", func() bool {
		have := map[uint32]bool{}
		for _, id := range c.Skills {
			have[id] = true
		}
		for _, id := range trained {
			if !have[id] {
				c.Skills = append(c.Skills, id)
			}
		}
		return true
	}) {
		return "", errors.New("skill update refused")
	}
	if err := s.RenameCharacterOffline(cfg.division, temp, cfg.name); err != nil {
		return "", fmt.Errorf("rename to %s: %w", cfg.name, err)
	}
	return backup, nil
}

/*
================
tempName

A creation-safe name no player can hold; the offline rename replaces it.
================
*/
func tempName() (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return "SROGM" + strings.ToUpper(hex.EncodeToString(suffix)), nil
}

/*
================
topSkills

Every racial skill group at the highest rank a character of this level can
learn, as root ids. With masteries "none" only mastery-free groups qualify.
================
*/
func topSkills(source *enterworld.TextdataSkills, raceKey string, level int64, allMasteries bool) ([]uint32, error) {
	prefix, raceByte := "SKILL_EU_", uint8(1)
	if raceKey == domain.RaceKeyChina {
		prefix, raceByte = "SKILL_CH_", 0
	}
	racial := map[uint32]bool{}
	for _, m := range domain.DefaultMasteries(raceKey) {
		racial[m.ID] = true
	}
	stat := int64(baseStat) + level - 1
	masteryLevel := int64(0)
	if allMasteries {
		masteryLevel = level
	}
	// ladders[group][rank] is that rank's row, for trainable racial roots.
	ladders := map[uint32]map[int64]enterworld.SkillRow{}
	for id := uint32(1); id <= maxSkillID; id++ {
		row, ok := source.SkillByID(id)
		if !ok || row.ChainSub || row.SPCost == 0 || !strings.HasPrefix(row.Codename, prefix) {
			continue
		}
		if row.RequiredRace != enterworld.SkillRaceAny && row.RequiredRace != raceByte {
			continue
		}
		if ladders[row.Group] == nil {
			ladders[row.Group] = map[int64]enterworld.SkillRow{}
		}
		ladders[row.Group][row.Level] = row
	}
	if len(ladders) == 0 {
		return nil, errors.New("no skill rows loaded; is the server game data built?")
	}
	eligible := func(row enterworld.SkillRow) bool {
		for _, requirement := range row.Masteries {
			if requirement.ID == 0 {
				continue
			}
			if !racial[requirement.ID] || requirement.Level > masteryLevel {
				return false
			}
		}
		return row.ReqStr <= stat && row.ReqInt <= stat
	}
	// top[group] is the highest rank whose ranks 1..n are all eligible.
	top := map[uint32]int64{}
	for group, ranks := range ladders {
		for n := int64(1); ; n++ {
			row, ok := ranks[n]
			if !ok || !eligible(row) {
				break
			}
			top[group] = n
		}
	}
	// A rank whose prerequisite group stops short is cut back, which can
	// cut other groups in turn: repeat until nothing changes.
	for changed := true; changed; {
		changed = false
		for group, n := range top {
			for n > 0 && !prerequisitesMet(ladders[group][n], top) {
				n--
				changed = true
			}
			if n == 0 {
				delete(top, group)
			} else {
				top[group] = n
			}
		}
	}
	ids := make([]uint32, 0, len(top))
	for group, n := range top {
		ids = append(ids, ladders[group][n].ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fmt.Printf("skills: %d of %d skill groups learnable at this level\n", len(ids), len(ladders))
	return ids, nil
}

/*
================
prerequisitesMet
================
*/
func prerequisitesMet(row enterworld.SkillRow, top map[uint32]int64) bool {
	for _, requirement := range row.Prerequisites {
		if requirement.ID != 0 && top[requirement.ID] < requirement.Level {
			return false
		}
	}
	return true
}

// accountRow is one entry of the Agent's account catalog.
type accountRow struct {
	ID           string `json:"id"`
	PasswordHash string `json:"passwordHash"`
}

/*
================
ensureAccount

Adds the account to the Agent catalog (cluster/accounts.json) when it is
missing. A generated password is written to sro-gm-logins/<account>.txt,
never printed. An existing account is left untouched.
================
*/
func ensureAccount(cfg config) error {
	path := filepath.Join(cfg.stateDir, "cluster", "accounts.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rows []accountRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, r := range rows {
		if r.ID == cfg.account {
			fmt.Printf("account: %s already exists, unchanged\n", cfg.account)
			return nil
		}
	}
	password := cfg.password
	if password == "" {
		secret := make([]byte, passwordBytes)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		password = base64.RawURLEncoding.EncodeToString(secret)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), accountBcryptCost)
	if err != nil {
		return err
	}
	rows = append(rows, accountRow{ID: cfg.account, PasswordHash: string(hash)})
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return err
	}
	if cfg.password != "" {
		fmt.Printf("account: %s added with the given password\n", cfg.account)
		return nil
	}
	logins := filepath.Join(cfg.stateDir, "sro-gm-logins")
	if err := os.MkdirAll(logins, 0o700); err != nil {
		return err
	}
	login := filepath.Join(logins, cfg.account+".txt")
	if err := os.WriteFile(login, []byte("account="+cfg.account+"\npassword="+password+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("account: %s added; password saved to %s\n", cfg.account, login)
	return nil
}

/*
================
grantGM

Adds division:name to cluster/gm-characters.txt, the list sro-nomad deploy
hands the GameWorld (SRO_GM_CHARACTERS). The file stays one comma-separated
line, the form store.ParseGMCharacters reads.
================
*/
func grantGM(cfg config) error {
	path := filepath.Join(cfg.stateDir, "cluster", "gm-characters.txt")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	entries, err := store.ParseGMCharacters(strings.TrimSpace(string(existing)))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	want := cfg.division + ":" + cfg.name
	values := make([]string, 0, len(entries)+1)
	for _, e := range entries {
		value := e.DivisionID + ":" + e.CharacterName
		if value == want {
			fmt.Println("gm: already listed")
			return nil
		}
		values = append(values, value)
	}
	values = append(values, want)
	if err := os.WriteFile(path, []byte(strings.Join(values, ",")+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println("gm: added", want)
	return nil
}

/*
================
verify

Reopens the store and checks what was persisted, not what was intended.
================
*/
func verify(dir string, options store.Options, cfg config, trained int) error {
	s, err := store.Open(dir, options)
	if err != nil {
		return err
	}
	defer s.Close()
	for _, c := range s.Characters().CharactersForDivision(cfg.division) {
		if c.Name != cfg.name {
			continue
		}
		if c.AccountID != cfg.account || c.Level == nil || *c.Level != cfg.level {
			return errors.New("verification failed: account or level differs")
		}
		want := int64(0)
		if cfg.masteries == "all" {
			want = cfg.level
		}
		for _, m := range c.Masteries {
			if m.Level != want {
				return fmt.Errorf("verification failed: mastery %d at %d", m.ID, m.Level)
			}
		}
		if len(c.Skills) < trained {
			return fmt.Errorf("verification failed: %d skills, want at least %d", len(c.Skills), trained)
		}
		fmt.Printf("VERIFIED %s account=%s level=%d masteries=%d skills=%d sp=%d\n",
			c.Name, c.AccountID, *c.Level, len(c.Masteries), len(c.Skills), *c.SkillPoints)
		return nil
	}
	return fmt.Errorf("%s missing after creation", cfg.name)
}
