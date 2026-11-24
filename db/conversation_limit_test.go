package db

import "testing"

func TestConversationActivityPageBounds(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := d.CreateConversation("mainagent", "page bounds", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteConversation(c.ID) })
	if _, err := d.Exec(`INSERT INTO conversation_activities(conversation_id,worker,kind,summary)
		SELECT $1,'mainagent','text','page boundary' FROM generate_series(1,1001)`, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 0, 1, 199, 200, 201} {
		want := limit
		if want <= 0 || want > 200 {
			want = 200
		}
		items, more, err := d.ConvActivityPage(c.ID, 0, limit)
		if err != nil || len(items) != want || !more {
			t.Fatalf("history limit=%d rows=%d more=%v err=%v", limit, len(items), more, err)
		}
	}
	seen := map[int64]bool{}
	before := int64(0)
	for pages := 0; ; pages++ {
		if pages > 6 {
			t.Fatal("history did not terminate")
		}
		items, more, err := d.ConvActivityPage(c.ID, before, 201)
		if err != nil || len(items) == 0 {
			t.Fatalf("history page: rows=%d err=%v", len(items), err)
		}
		for i, item := range items {
			if seen[item.ID] || (i > 0 && items[i-1].ID >= item.ID) {
				t.Fatal("history overlap or unordered page")
			}
			seen[item.ID] = true
		}
		if !more {
			break
		}
		before = items[0].ID
	}
	if len(seen) != 1001 {
		t.Fatalf("history coverage=%d", len(seen))
	}
	for _, limit := range []int{-1, 0, 1, 999, 1000, 1001} {
		want := limit
		if want <= 0 {
			want = 500
		}
		want = min(want, 1000)
		items, cursor, err := d.ConvActivityList(c.ID, 0, limit)
		if err != nil || len(items) != want || cursor != items[len(items)-1].ID {
			t.Fatalf("tail limit=%d rows=%d cursor=%d err=%v", limit, len(items), cursor, err)
		}
	}
	first, cursor, err := d.ConvActivityList(c.ID, 0, 1001)
	if err != nil || len(first) != 1000 {
		t.Fatalf("tail first: rows=%d err=%v", len(first), err)
	}
	last, end, err := d.ConvActivityList(c.ID, cursor, 1001)
	if err != nil || len(last) != 1 || last[0].ID <= cursor || end != last[0].ID {
		t.Fatalf("tail second: rows=%d cursor=%d err=%v", len(last), end, err)
	}
	empty, same, err := d.ConvActivityList(c.ID, end, 1001)
	if err != nil || len(empty) != 0 || same != end {
		t.Fatalf("tail end: rows=%d cursor=%d err=%v", len(empty), same, err)
	}
}
