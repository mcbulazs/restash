package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// MetricsHandler serves Prometheus metrics: per PC, its owner, whether
// it's online, its last good backup, its size and its problems. Only names
// and numbers, never anything about files. Serve it on an internal port.
func (s *Server) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		states, pcs, err := s.statuses(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		users, err := s.store.Users(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		owner := map[string]string{}
		for _, u := range users {
			owner[u.ID] = u.Name
		}
		sort.Slice(pcs, func(i, j int) bool { return pcs[i].Name < pcs[j].Name })

		var b strings.Builder
		metric := func(name, help, typ string) {
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		}
		labels := func(pcName, ownerName string, extra ...string) string {
			l := fmt.Sprintf(`pc="%s",owner="%s"`, esc(pcName), esc(ownerName))
			for i := 0; i+1 < len(extra); i += 2 {
				l += fmt.Sprintf(`,%s="%s"`, extra[i], esc(extra[i+1]))
			}
			return "{" + l + "}"
		}

		metric("restash_people", "Accounts.", "gauge")
		fmt.Fprintf(&b, "restash_people %d\n", len(users))

		metric("restash_person_stored_bytes", "Space a person's PCs take together.", "gauge")
		stored := map[string]int64{}
		for _, p := range pcs {
			stored[p.UserID] += s.sizes.size(p.ID)
		}
		for _, u := range users {
			fmt.Fprintf(&b, "restash_person_stored_bytes{owner=\"%s\"} %d\n", esc(u.Name), stored[u.ID])
		}
		metric("restash_person_limit_bytes", "A person's storage limit (0: none).", "gauge")
		for _, u := range users {
			fmt.Fprintf(&b, "restash_person_limit_bytes{owner=\"%s\"} %d\n", esc(u.Name), u.StorageLimit)
		}

		metric("restash_pc_online", "1 if the PC's agent is connected.", "gauge")
		for _, p := range pcs {
			fmt.Fprintf(&b, "restash_pc_online%s %d\n", labels(p.Name, owner[p.UserID]), boolInt(s.hub.online(p.ID)))
		}
		metric("restash_pc_last_backup_timestamp_seconds", "Time of the PC's last good backup (0 if none).", "gauge")
		for _, p := range pcs {
			t := int64(0)
			if lo := states[p.ID].LastOK; !lo.IsZero() {
				t = lo.Unix()
			}
			fmt.Fprintf(&b, "restash_pc_last_backup_timestamp_seconds%s %d\n", labels(p.Name, owner[p.UserID]), t)
		}
		metric("restash_pc_stored_bytes", "Space the PC's backups take on the server.", "gauge")
		for _, p := range pcs {
			fmt.Fprintf(&b, "restash_pc_stored_bytes%s %d\n", labels(p.Name, owner[p.UserID]), s.sizes.size(p.ID))
		}
		metric("restash_pc_problems", "Ongoing problems by kind: failed, late, quiet.", "gauge")
		for _, p := range pcs {
			count := map[string]int{"failed": 0, "late": 0, "quiet": 0}
			for _, pr := range states[p.ID].Problems {
				count[pr.Kind]++
			}
			for _, k := range []string{"failed", "late", "quiet"} {
				fmt.Fprintf(&b, "restash_pc_problems%s %d\n", labels(p.Name, owner[p.UserID], "kind", k), count[k])
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(b.String()))
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// esc escapes a Prometheus label value.
func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
