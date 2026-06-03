package services

import (
	"testing"
)

func makeResult(filePath, title, artist string) scanExtractResult {
	return scanExtractResult{
		item:     scanProcessItem{filePath: filePath},
		metadata: &Metadata{Title: title, Artist: artist},
	}
}

func TestFixSpamTags_NormalAlbum(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/album/01.mp3", "Track 1", "Artist"),
		makeResult("/music/album/02.mp3", "Track 2", "Artist"),
		makeResult("/music/album/03.mp3", "Track 3", "Artist"),
		makeResult("/music/album/04.mp3", "Track 4", "Artist"),
	}
	fixSpamTags(results)

	for i, r := range results {
		if r.metadata.Artist != "Artist" {
			t.Errorf("result[%d] artist changed to %q, want %q", i, r.metadata.Artist, "Artist")
		}
	}
	if results[0].metadata.Title != "Track 1" {
		t.Errorf("result[0] title changed to %q, want %q", results[0].metadata.Title, "Track 1")
	}
}

func TestFixSpamTags_SpamDetected(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/xiyouji/087西游记第八十七回.m4a", "星哥会员免费", "加微信：36903863"),
		makeResult("/music/xiyouji/084西游记第八十四回.m4a", "星哥会员免费", "加微信：36903863"),
		makeResult("/music/xiyouji/088西游记第八十八回.m4a", "星哥会员免费", "加微信：36903863"),
		makeResult("/music/xiyouji/085西游记第八十五回.m4a", "星哥会员免费", "加微信：36903863"),
	}
	fixSpamTags(results)

	for i, r := range results {
		if r.metadata.Title == "星哥会员免费" {
			t.Errorf("result[%d] title not fixed, still %q", i, r.metadata.Title)
		}
		if r.metadata.Artist != "" {
			t.Errorf("result[%d] artist not cleared, got %q", i, r.metadata.Artist)
		}
	}
	if results[0].metadata.Title != "087西游记第八十七回" {
		t.Errorf("result[0] title = %q, want %q", results[0].metadata.Title, "087西游记第八十七回")
	}
}

func TestFixSpamTags_BelowThreshold(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/dir/a.mp3", "Same", "Same"),
		makeResult("/music/dir/b.mp3", "Same", "Same"),
	}
	fixSpamTags(results)

	if results[0].metadata.Title != "Same" {
		t.Errorf("should not fix when count < 3, got title=%q", results[0].metadata.Title)
	}
}

func TestFixSpamTags_ExactlyAtThreshold(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/dir/a.mp3", "Spam", "Ad"),
		makeResult("/music/dir/b.mp3", "Spam", "Ad"),
		makeResult("/music/dir/c.mp3", "Spam", "Ad"),
		makeResult("/music/dir/d.mp3", "Real Title", "Real Artist"),
		makeResult("/music/dir/e.mp3", "Another", "Another"),
	}
	fixSpamTags(results)

	if results[0].metadata.Title != "a" {
		t.Errorf("result[0] title = %q, want %q", results[0].metadata.Title, "a")
	}
	if results[3].metadata.Title != "Real Title" {
		t.Errorf("result[3] should not be changed, got %q", results[3].metadata.Title)
	}
	if results[4].metadata.Title != "Another" {
		t.Errorf("result[4] should not be changed, got %q", results[4].metadata.Title)
	}
}

func TestFixSpamTags_NotOver50Percent(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/dir/a.mp3", "Same", "Same"),
		makeResult("/music/dir/b.mp3", "Same", "Same"),
		makeResult("/music/dir/c.mp3", "Same", "Same"),
		makeResult("/music/dir/d.mp3", "X", "Y"),
		makeResult("/music/dir/e.mp3", "A", "B"),
		makeResult("/music/dir/f.mp3", "C", "D"),
	}
	fixSpamTags(results)

	if results[0].metadata.Title != "Same" {
		t.Errorf("should not fix at exactly 50%%, got %q", results[0].metadata.Title)
	}
}

func TestFixSpamTags_CrossDirIndependent(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/dir1/a.mp3", "Spam", "Ad"),
		makeResult("/music/dir1/b.mp3", "Spam", "Ad"),
		makeResult("/music/dir1/c.mp3", "Spam", "Ad"),
		makeResult("/music/dir2/x.mp3", "Spam", "Ad"),
		makeResult("/music/dir2/y.mp3", "Good", "Good"),
	}
	fixSpamTags(results)

	if results[0].metadata.Title != "a" {
		t.Errorf("dir1 should be fixed, got %q", results[0].metadata.Title)
	}
	if results[3].metadata.Title != "Spam" {
		t.Errorf("dir2 should not be fixed (only 1 match in that dir), got %q", results[3].metadata.Title)
	}
}

func TestFixSpamTags_EmptyTags(t *testing.T) {
	results := []scanExtractResult{
		makeResult("/music/dir/a.mp3", "", ""),
		makeResult("/music/dir/b.mp3", "", ""),
		makeResult("/music/dir/c.mp3", "", ""),
		makeResult("/music/dir/d.mp3", "", ""),
	}
	fixSpamTags(results)

	if results[0].metadata.Title != "" {
		t.Errorf("empty tags should not be treated as spam, got %q", results[0].metadata.Title)
	}
}
