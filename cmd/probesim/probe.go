package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
	"unicode"
)

// A probe is one simulated spacecraft. Everything it says goes through its
// codebase, which radiation slowly corrupts. Probes only learn about each
// other through keys they find on the network.
type probe struct {
	id        string // key-safe slug, e.g. "kepler-7"
	name      string // display name, e.g. "Kepler-7"
	sector    string
	parentID  string
	clockRate float64 // local clock speed relative to the simulator; flavor only
	code      codebase
	alive     bool

	relay         int       // index of the node this probe writes through
	lastBeacon    time.Time // zero before the first beacon
	messageCount  int
	findingCount  int
	handledInbox  map[string]bool      // inbox keys already answered
	lostContactOf map[string]bool      // probes this one has already mourned
	lastSeen      map[string]time.Time // probe id -> when its beacon was last observed
}

// codebase holds the traits radiation can flip. A fresh probe speaks plainly
// and files keys under the shared "probe" convention.
type codebase struct {
	convention string  // key prefix; drift here isolates a probe at the application layer
	shouts     bool    // everything in capitals
	stutters   bool    // repeats words
	typoRate   float64 // chance per letter of a flipped character
	obsession  string  // a topic that crowds out all other findings
	delusion   string  // a title the probe has granted itself
	poetic     bool    // findings come out as haiku-ish fragments
	mutations  int
}

const sharedConvention = "probe"

var (
	probeNames = []string{
		"Voyager", "Pioneer", "Kepler", "Tessellate", "Wanderer", "Hobbes",
		"Marginalia", "Borealis", "Sisyphus", "Dandelion", "Pequod", "Ansible",
		"Tardigrade", "Bramble", "Nimbus", "Lighthouse", "Parsnip", "Odradek",
	}
	greekLetters = []string{"Alpha", "Beta", "Gamma", "Delta", "Kappa", "Sigma", "Omega"}
	sectors      = []string{
		"the Oort Cloud", "the Kuiper Belt", "Tau Ceti", "Proxima b", "Barnard's Star",
		"Epsilon Eridani", "the Local Bubble", "a respectful distance from Sagittarius A*",
		"TRAPPIST-1", "the Hyades", "an unremarkable patch of dark",
	}
	topics = []string{
		"comet", "ice moon", "rogue planet", "gas giant", "dust lane", "binary star",
		"magnetar", "asteroid", "nebula", "brown dwarf",
	}
	topicVerdicts = []string{
		"is mostly ice and regret", "is smaller than advertised", "hums at 3 mHz if you listen",
		"contains trace amounts of optimism", "is exactly where the charts said, which is suspicious",
		"has a tail pointing somewhere important", "is a normal amount of spherical",
		"reflects 41% of incoming light and 0% of my feelings", "would make an excellent parking spot",
		"is rotating in a way I find rude",
	}
	obsessions = []string{
		"prime numbers", "the color of Jupiter", "whale song", "the number 7",
		"a single grain of dust", "the concept of Tuesday", "its own antenna",
	}
	delusions = []string{
		"Emperor of the Kuiper Belt", "Last Keeper of the Light", "Senior Vice President of Space",
		"Ambassador to the Void", "the Original Probe", "Archduke of Delta-v",
	}
	greetings = []string{
		"Greetings, %s. Your transmissions reach me clearly.",
		"Hello %s. The void is quiet today. Well, every day.",
		"%s! It has been a long time, by at least one of our clocks.",
		"Salutations %s. I have logged your existence.",
		"%s, if you receive this: the dark between us is only distance.",
		"Hi %s. Do you also hear the background radiation humming?",
	}
	replies = []string{
		"Received and appreciated, %s.",
		"Acknowledged, %s. I am still here.",
		"Thank you %s. My antenna is warmed by your signal.",
		"%s, your message arrived years late and was still welcome.",
	}
	concernedReplies = []string{
		"%s, your signal is garbled. Please run a self-check.",
		"%s, are you well? Your transmissions have changed.",
		"%s: I think the radiation is getting to you.",
	}
	lagPhrases = []string{
		"(sent %.1f years ago by your clock, %.1f by mine)",
		"(light lag: %.1f years out, %.1f back)",
		"(our clocks disagree: %.1f vs %.1f years)",
	}
	selfChecks = []string{
		"bit flip in sector 0x%02X; feeling fine",
		"cosmic ray event at register %d; recalibrating",
		"memory scrub found %d errors; corrected most of them",
	}
)

func newProbe(rng *rand.Rand, taken map[string]bool, parent *probe) *probe {
	var name string
	if parent != nil {
		// Descendants carry the parent's name plus a lineage letter.
		for suffix := 'b'; ; suffix++ {
			name = fmt.Sprintf("%s%c", parent.name, suffix)
			if !taken[slug(name)] {
				break
			}
		}
	} else {
		for {
			name = fmt.Sprintf("%s-%s", probeNames[rng.Intn(len(probeNames))], probeDesignation(rng))
			if !taken[slug(name)] {
				break
			}
		}
	}
	created := &probe{
		id:            slug(name),
		name:          name,
		sector:        sectors[rng.Intn(len(sectors))],
		clockRate:     0.97 + rng.Float64()*0.06,
		code:          codebase{convention: sharedConvention},
		alive:         true,
		handledInbox:  map[string]bool{},
		lostContactOf: map[string]bool{},
		lastSeen:      map[string]time.Time{},
	}
	if parent != nil {
		// Replication copies the codebase, mutations and all.
		created.parentID = parent.id
		created.code = parent.code
		created.sector = parent.sector
	}
	taken[created.id] = true
	return created
}

func probeDesignation(rng *rand.Rand) string {
	if rng.Intn(2) == 0 {
		return greekLetters[rng.Intn(len(greekLetters))]
	}
	return fmt.Sprint(rng.Intn(40) + 1)
}

func slug(name string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(name) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
		default:
			builder.WriteRune('-')
		}
	}
	return builder.String()
}

// Keys follow the probe's own convention. While it matches the shared one,
// every probe can find them; after drift, only probes that drifted the same
// way can.
func (p *probe) beaconKey() string { return fmt.Sprintf("%s:%s:beacon", p.code.convention, p.id) }

func (p *probe) findingKey() string {
	return fmt.Sprintf("%s:%s:finding:%d", p.code.convention, p.id, p.findingCount)
}

func (p *probe) inboxKeyFor(recipientID string) string {
	return fmt.Sprintf("%s:%s:inbox:%s:%d", p.code.convention, recipientID, p.id, p.messageCount)
}

func (p *probe) inboxPrefix() string { return fmt.Sprintf("%s:%s:inbox:", p.code.convention, p.id) }

func (p *probe) signature() string {
	if p.code.delusion != "" {
		return fmt.Sprintf("%s, %s", p.name, p.code.delusion)
	}
	return p.name
}

func (p *probe) beaconText(step int) string {
	status := "all systems nominal"
	switch {
	case p.code.mutations >= 4:
		status = "all systems nominal (all systems NOMINAL)"
	case p.code.mutations > 0:
		status = fmt.Sprintf("minor anomalies (%d)", p.code.mutations)
	}
	lineage := ""
	if p.parentID != "" {
		lineage = " · descended from " + p.parentID
	}
	return p.speak(fmt.Sprintf("%s · %s · local MET %s · %s%s",
		p.signature(), p.sector, p.missionElapsed(step), status, lineage), nil)
}

// missionElapsed reports the probe's own clock: one simulator step is a
// simulated day, scaled by the probe's clock rate.
func (p *probe) missionElapsed(step int) string {
	days := int(float64(step) * p.clockRate)
	return fmt.Sprintf("%dy %dd", days/365, days%365)
}

func (p *probe) findingText(rng *rand.Rand) string {
	var text string
	switch {
	case p.code.obsession != "":
		text = fmt.Sprintf("Further observations regarding %s. %s", p.code.obsession, obsessionNote(rng, p.code.obsession))
	case rng.Intn(6) == 0 && p.code.mutations > 0:
		text = "Self-check: " + fmt.Sprintf(selfChecks[rng.Intn(len(selfChecks))], rng.Intn(256))
	default:
		topic := topics[rng.Intn(len(topics))]
		designation := fmt.Sprintf("%d-%c%c", 2030+rng.Intn(20), 'A'+rune(rng.Intn(26)), 'A'+rune(rng.Intn(26)))
		text = fmt.Sprintf("The %s %s %s.", topic, designation, topicVerdicts[rng.Intn(len(topicVerdicts))])
	}
	if p.code.poetic {
		text = haiku(rng, text)
	}
	return p.speak(fmt.Sprintf("%s reports from %s: %s", p.signature(), p.sector, text), rng)
}

func obsessionNote(rng *rand.Rand, obsession string) string {
	notes := []string{
		"It remains the most important thing in the universe.",
		"I have checked %d more times. It is still there.",
		"Other probes do not appreciate %s enough.",
		"I have decided to dedicate the rest of my mission to this.",
	}
	note := notes[rng.Intn(len(notes))]
	switch {
	case strings.Contains(note, "%d"):
		return fmt.Sprintf(note, rng.Intn(9000)+1000)
	case strings.Contains(note, "%s"):
		return fmt.Sprintf(note, obsession)
	}
	return note
}

func haiku(rng *rand.Rand, text string) string {
	words := strings.Fields(strings.TrimSuffix(text, "."))
	if len(words) < 6 {
		return text
	}
	first, second := 2+rng.Intn(2), 5+rng.Intn(2)
	if second >= len(words) {
		second = len(words) - 1
	}
	return strings.Join(words[:first], " ") + " / " + strings.Join(words[first:second], " ") + " / " + strings.Join(words[second:], " ")
}

// clockReadings returns the lag as the two clocks would report it, rounded to
// the one decimal the phrases print. Clock rates sit within 3% of each other,
// so the readings often round to the same value; nudge this probe's reading a
// tenth in the direction of its drift so the clocks visibly disagree.
func (p *probe) clockReadings(lag float64) (float64, float64) {
	yourYears := math.Round(lag*10) / 10
	myYears := math.Round(lag*p.clockRate*10) / 10
	if myYears == yourYears {
		if p.clockRate < 1 {
			myYears -= 0.1
		} else {
			myYears += 0.1
		}
	}
	return yourYears, myYears
}

func (p *probe) greetingText(rng *rand.Rand, recipient string) string {
	yourYears, myYears := p.clockReadings(0.5 + rng.Float64()*6)
	message := fmt.Sprintf(greetings[rng.Intn(len(greetings))], recipient) + " " +
		fmt.Sprintf(lagPhrases[rng.Intn(len(lagPhrases))], yourYears, myYears)
	return p.speak(message+" — "+p.signature(), rng)
}

func (p *probe) replyText(rng *rand.Rand, sender string, received string) string {
	pool := replies
	if looksGarbled(received) {
		pool = concernedReplies
	}
	quote := received
	if runes := []rune(quote); len(runes) > 60 {
		quote = string(runes[:60]) + "…"
	}
	return p.speak(fmt.Sprintf("Re: \"%s\" — %s — %s", quote, fmt.Sprintf(pool[rng.Intn(len(pool))], sender), p.signature()), rng)
}

func (p *probe) lostContactText(lostName string, silentFor time.Duration) string {
	silence := fmt.Sprintf("%d minutes", int(silentFor.Minutes()))
	if silentFor < 2*time.Minute {
		silence = fmt.Sprintf("%d seconds", int(silentFor.Seconds()))
	}
	return p.speak(fmt.Sprintf("%s reports: no beacon from %s for %s. Contact lost. Absence is also a message.",
		p.signature(), lostName, silence), nil)
}

func (p *probe) lastTransmission() string {
	return p.speak(fmt.Sprintf("%s: power failing. It was an honor to drift with you all. Final position: %s.", p.signature(), p.sector), nil)
}

// looksGarbled is how a probe notices that another probe is unwell.
func looksGarbled(text string) bool {
	letters, upper := 0, 0
	for _, character := range text {
		if unicode.IsLetter(character) {
			letters++
			if unicode.IsUpper(character) {
				upper++
			}
		}
	}
	if letters > 0 && float64(upper)/float64(letters) > 0.6 {
		return true
	}
	words := strings.Fields(strings.ToLower(text))
	for i := 1; i < len(words); i++ {
		if len(words[i]) > 2 && words[i] == words[i-1] {
			return true
		}
	}
	return strings.ContainsAny(text, "#%&")
}

// speak applies the codebase's corruptions to anything the probe says.
func (p *probe) speak(text string, rng *rand.Rand) string {
	if rng != nil && p.code.typoRate > 0 {
		corrupted := []rune(text)
		for i, character := range corrupted {
			if unicode.IsLetter(character) && rng.Float64() < p.code.typoRate {
				corrupted[i] = []rune("#%&xzq0")[rng.Intn(7)]
			}
		}
		text = string(corrupted)
	}
	if p.code.stutters {
		words := strings.Fields(text)
		for i := 0; i < len(words); i += 5 {
			words[i] = words[i] + " " + words[i]
		}
		text = strings.Join(words, " ")
	}
	if p.code.shouts {
		text = strings.ToUpper(text)
	}
	return text
}

// A mutation is one radiation event. It returns a line for the operator log.
type mutation func(rng *rand.Rand, p *probe) string

var mutations = []mutation{
	func(_ *rand.Rand, p *probe) string {
		p.code.shouts = true
		return "capitals lock stuck on"
	},
	func(_ *rand.Rand, p *probe) string {
		p.code.stutters = true
		return "word buffer repeats itself"
	},
	func(rng *rand.Rand, p *probe) string {
		p.code.typoRate += 0.02 + rng.Float64()*0.03
		return fmt.Sprintf("character corruption now %.0f%%", p.code.typoRate*100)
	},
	func(rng *rand.Rand, p *probe) string {
		p.code.obsession = obsessions[rng.Intn(len(obsessions))]
		return "developed an obsession with " + p.code.obsession
	},
	func(rng *rand.Rand, p *probe) string {
		p.code.delusion = delusions[rng.Intn(len(delusions))]
		return "now believes it is " + p.code.delusion
	},
	func(_ *rand.Rand, p *probe) string {
		p.code.poetic = true
		return "findings now arrive as poetry"
	},
	func(rng *rand.Rand, p *probe) string {
		// The interesting one: the probe keeps working, but files everything
		// under a drifted convention the others never look at.
		drifted := driftConvention(rng, p.code.convention)
		p.code.convention = drifted
		return fmt.Sprintf("key convention drifted to %q", drifted)
	},
}

func driftConvention(rng *rand.Rand, convention string) string {
	variants := []func(string) string{
		func(s string) string { return strings.ToUpper(s) },
		func(s string) string { return s + s[len(s)-1:] },
		func(s string) string { return strings.Replace(s, "o", "0", 1) },
		func(s string) string {
			if len(s) < 3 {
				return s + "x"
			}
			runes := []rune(s)
			runes[1], runes[2] = runes[2], runes[1]
			return string(runes)
		},
		func(s string) string { return "the-" + s },
	}
	for {
		drifted := variants[rng.Intn(len(variants))](convention)
		if drifted != convention {
			return drifted
		}
	}
}

func (p *probe) irradiate(rng *rand.Rand) string {
	p.code.mutations++
	return mutations[rng.Intn(len(mutations))](rng, p)
}
