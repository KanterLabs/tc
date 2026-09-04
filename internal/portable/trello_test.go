package portable

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConvertTrelloProducesStablePortableArchiveAndWarnings(t *testing.T) {
	data := []byte(`{
      "id":"board-1", "name":"Launch Board", "desc":"Ship it",
      "dateLastActivity":"2026-09-04T00:00:00Z",
      "lists":[{"id":"list-1","name":"To do"},{"id":"list-2","name":"Done"}],
      "cards":[{"id":"card-1","name":"Ship feature","desc":"Details","idList":"list-1","due":"2026-09-10T12:00:00Z","labels":[{"id":"label-1","name":"Urgent","color":"red"}],"idMembers":["member-1"],"attachments":[{"id":"attachment-1"}]}],
      "actions":[{"id":"action-1","type":"commentCard","date":"2026-09-04T01:00:00Z","data":{"text":"A comment","card":{"id":"card-1"}},"memberCreator":{"id":"member-1","fullName":"Member One"}},{"id":"action-2","type":"updateCard","date":"2026-09-04T01:00:00Z"}],
      "checklists":[{"id":"checklist-1"}], "memberships":[{"id":"membership-1"}]
    }`)
	archive, warnings, err := ConvertTrello(data)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Format != "helm.portable" || archive.Version != 1 || len(archive.Projects) != 1 || len(archive.Columns) != 2 || len(archive.Tasks) != 1 || len(archive.Labels) != 1 || len(archive.Comments) != 1 || len(archive.Actors) != 1 {
		t.Fatalf("archive counts/projects = %+v cols=%d tasks=%d labels=%d comments=%d actors=%d", archive.Projects, len(archive.Columns), len(archive.Tasks), len(archive.Labels), len(archive.Comments), len(archive.Actors))
	}
	if archive.Tasks[0].DueAt == nil || *archive.Tasks[0].DueAt != "2026-09-10T12:00:00Z" || len(archive.Relationships.TaskLabels) != 1 {
		t.Fatalf("task conversion = %+v relationships=%+v", archive.Tasks[0], archive.Relationships)
	}
	joinedWarnings := strings.Join(warnings, "\n")
	for _, expected := range []string{"memberships", "checklists", "non-comment actions", "card members", "attachments"} {
		if !strings.Contains(joinedWarnings, expected) {
			t.Fatalf("warnings %q do not mention %q", joinedWarnings, expected)
		}
	}
	second, _, err := ConvertTrello(data)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(archive)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("same Trello input produced a different portable archive")
	}
}

func TestConvertTrelloCompletedCardsAndUnsupportedFields(t *testing.T) {
	data := []byte(`{
      "id":"board-done", "name":"Done board", "closed":false,
      "prefs":{"permissionLevel":"private"}, "preferences":{"calendar":true}, "unknownBoardField":true,
      "dateLastActivity":"2026-09-04T03:00:00Z",
      "lists":[{"id":"list-done","name":"Done","closed":false,"unknownListField":"ignored"}],
      "cards":[{"id":"card-done","name":"Shipped","idList":"list-done","dateLastActivity":"2026-09-04T04:00:00Z","closed":false,"pos":42,"idLabels":[],"dueComplete":true,"unknownCardField":"ignored"}],
      "actions":[{"id":"action-done","type":"commentCard","date":"2026-09-04T05:00:00Z","data":{"text":"Shipped","card":{"id":"card-done","unknownCardRef":"ignored"},"unknownActionDataField":true},"memberCreator":{"id":"member-done","fullName":"Done member","unknownMemberField":true},"unknownActionField":true}]
    }`)
	archive, warnings, err := ConvertTrello(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Tasks) != 1 || archive.Tasks[0].CompletedAt == nil || *archive.Tasks[0].CompletedAt != "2026-09-04T04:00:00Z" {
		t.Fatalf("completed Trello card = %+v", archive.Tasks)
	}
	if len(archive.Columns) != 1 || archive.Columns[0].SemanticState != "completed" {
		t.Fatalf("completed Trello column = %+v", archive.Columns)
	}
	joined := strings.Join(warnings, "\n")
	for _, field := range []string{"closed", "pos", "idLabels", "dueComplete", "prefs", "preferences", "unknownBoardField", "unknownListField", "unknownCardField", "unknownActionField", "unknownActionDataField", "unknownCardRef", "unknownMemberField"} {
		if !strings.Contains(joined, field) {
			t.Fatalf("warnings %q do not mention unsupported field %q", joined, field)
		}
	}
}
