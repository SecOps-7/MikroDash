package rbac

import (
	"encoding/json"
	"os"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

// The live differential: every answer this resolver gives, against every answer
// rbac.js gives, over a REAL grant graph.
//
// The unit tests beside this one prove the algorithm on graphs I invented, and
// that is exactly their weakness — I invented them from the same reading of
// rbac.js that produced the implementation, so a misreading would be reproduced
// faithfully on both sides and look like agreement. This compares against the
// other implementation, on data neither of us chose.
//
// SKIPPED BY DEFAULT, AND NOT BECAUSE IT IS OPTIONAL. Its inputs name real user,
// router and site ids, so neither can be committed to a public repository.
// Generate them on demand:
//
//	# 1. Node's answers, from the running app container. The `[db] opened` line
//	#    it prints first has to be stripped; the JSON starts at the first brace.
//	docker exec mikrodash node -e '
//	  const Users=require("/app/src/users.js"), Routers=require("/app/src/routers.js");
//	  const Pages=require("/app/src/pages.js"), Rbac=require("/app/src/rbac.js");
//	  require("/app/src/db.js").open(); Rbac.init && Rbac.init({isModern:()=>true});
//	  const out=[];
//	  for (const u of Users.listUsersSync())
//	    for (const r of Routers.loadAll())
//	      for (const p of Pages.KEYS)
//	        for (const a of ["read","write"])
//	          out.push({userId:u.id, routerId:r.id, page:p, access:a,
//	                    allowed: !!Rbac.canPage({userId:u.id}, p, a, r.id)});
//	  console.log(JSON.stringify({out}));'
//
//	# 2. a COPY of /data. NEVER the live directory: db.Open opens read-write.
//	docker cp mikrodash:/data/. /tmp/data-copy/
//
//	# 3. run it
//	MIKRODASH_DATA=/tmp/data-copy RBAC_NODE_ANSWERS=/tmp/node-rbac.json \
//	  go test ./internal/rbac/ -run Live -v
type nodeAnswers struct {
	Out []struct {
		UserID   string `json:"userId"`
		RouterID string `json:"routerId"`
		Page     string `json:"page"`
		Access   string `json:"access"`
		Allowed  bool   `json:"allowed"`
	} `json:"out"`
}

func TestLiveAgainstNodeAnswers(t *testing.T) {
	dataDir := os.Getenv("MIKRODASH_DATA")
	answersPath := os.Getenv("RBAC_NODE_ANSWERS")
	if dataDir == "" || answersPath == "" {
		t.Skip("set MIKRODASH_DATA (a COPY of /data) and RBAC_NODE_ANSWERS to run the live differential")
	}

	raw, err := os.ReadFile(answersPath)
	if err != nil {
		t.Fatalf("read %s: %v", answersPath, err)
	}
	var want nodeAnswers
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse %s: %v", answersPath, err)
	}
	if len(want.Out) == 0 {
		t.Fatal("no cases — an empty differential asserts nothing")
	}

	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	database, err := db.Open(dataDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	resolver := New(database, func() []Router {
		list, _ := st.Routers()
		out := make([]Router, 0, len(list))
		for _, r := range list {
			out = append(out, Router{ID: r.ID, SiteIDs: store.RouterSiteIDs(r)})
		}
		return out
	})

	var mismatches, allowed int
	for _, c := range want.Out {
		got, err := resolver.CanPage(c.UserID, c.Page, c.Access, c.RouterID)
		if err != nil {
			t.Fatalf("CanPage: %v", err)
		}
		if c.Allowed {
			allowed++
		}
		if got != c.Allowed {
			mismatches++
			if mismatches <= 20 {
				// Ids are truncated deliberately: this output can end up in a
				// terminal someone pastes, and a full user id identifies a
				// person.
				t.Errorf("user %.8s router %.8s %s:%s — Go says %v, Node says %v",
					c.UserID, c.RouterID, c.Page, c.Access, got, c.Allowed)
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d answers disagree with rbac.js", mismatches, len(want.Out))
	}
	t.Logf("%d answers match rbac.js exactly (%d allowed, %d denied)",
		len(want.Out), allowed, len(want.Out)-allowed)
}
