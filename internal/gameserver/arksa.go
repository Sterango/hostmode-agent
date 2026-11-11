package gameserver

import (
	"fmt"
	"strings"
)

// ARK: Survival Ascended ships only a Windows dedicated server, so it runs under
// Proton in the community image acekorneya/asa_server (MIT,
// github.com/Acekorneya/Ark-Survival-Ascended-Server). The 2_1 image runs as
// uid 7777, installs/updates the game via SteamCMD on boot and is configured
// entirely through env vars. ASA has no A2S (it uses EOS), so player counts come
// from RCON ListPlayers.
const arksaImage = "acekorneya/asa_server:2_1_latest"

// ArkSAMaps are the official maps the panel offers (value = MAP_NAME).
var ArkSAMaps = []string{
	"TheIsland_WP", "ScorchedEarth_WP", "TheCenter_WP", "Aberration_WP",
	"Extinction_WP", "Ragnarok_WP", "Astraeus_WP", "Valguero_WP",
}

func init() {
	Specs["arksa"] = GameSpec{
		Key: "arksa", Label: "ARK: Survival Ascended", Tag: "", Name: "",
		// game (UDP), RCON (TCP)
		Ports: []Port{{0, "udp"}, {1, "tcp"}}, QueryOffset: -1, PortCount: 4,
		Container: &ContainerSpec{
			Image:    arksaImage,
			UID:      7777,
			DataDirs: []string{"arkserver"},
			Mounts: func(hostData string) []string {
				return []string{hostData + "/arkserver:/home/pok/arkserver"}
			},
			Env: func(s *Server, st map[string]interface{}) []string {
				battleye := "FALSE"
				if settingBool(st, "battleye", false) {
					battleye = "TRUE"
				}
				return []string{
					"INSTANCE_NAME=" + s.UUID,
					"SESSION_NAME=" + argSafe(s.Name),
					"MAP_NAME=" + settingStr(st, "map", "TheIsland_WP"),
					fmt.Sprintf("ASA_PORT=%d", s.BasePort),
					fmt.Sprintf("RCON_PORT=%d", s.BasePort+1),
					fmt.Sprintf("MAX_PLAYERS=%d", s.MaxPlayers),
					"SERVER_ADMIN_PASSWORD=" + s.Secret("admin_password"),
					"SERVER_PASSWORD=" + alnum(settingStr(st, "server_password", "")),
					"CLUSTER_ID=" + settingStr(st, "cluster_id", "hostmode"),
					"BATTLEEYE=" + battleye,
					"RCON_ENABLED=TRUE",
					"API=FALSE",
					"UPDATE_SERVER=TRUE",
					"RANDOM_STARTUP_DELAY=FALSE",
					"TZ=UTC",
				}
			},
			RCONOffset:     1,
			PlayersCommand: "ListPlayers",
			CountPlayers:   countARKPlayers,
			SaveCommand:    "SaveWorld",
			StopTimeout:    120,
		},
	}
}

// countARKPlayers parses ListPlayers output: one "N. Name, ID" line per player,
// or "No Players Connected".
func countARKPlayers(out string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && strings.Contains(l, ".") && strings.Contains(l, ",") {
			n++
		}
	}
	return n
}

// alnum keeps only letters and digits (ASA rejects other characters in passwords).
func alnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
