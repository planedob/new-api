package model

import "testing"

func TestChannelCacheCreatesMissingGroupBucket(t *testing.T) {
	cache := make(map[string]map[string][]int)
	group := "new-group-without-ability"
	if _, ok := cache[group]; ok {
		t.Fatal("test precondition: group bucket already exists")
	}

	// A channel group may transiently exist before its ability rows are
	// available; the cache builder must allocate its bucket before use.
	ensureChannelCacheGroup(cache, group)
	cache[group]["gpt-5.5"] = append(cache[group]["gpt-5.5"], 70)

	if got := cache[group]["gpt-5.5"]; len(got) != 1 || got[0] != 70 {
		t.Fatalf("unexpected cached channel ids: %#v", got)
	}
}
