// Package portable contains format-specific adapters that produce Helm's
// versioned portability envelope. Adapters live outside store so the core
// importer never grows assumptions about a third-party API.
package portable

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/KanterLabs/helm/internal/store"
)

type trelloBoard struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Desc             string            `json:"desc"`
	Closed           bool              `json:"closed"`
	Prefs            json.RawMessage   `json:"prefs"`
	Preferences      json.RawMessage   `json:"preferences"`
	DateLastActivity string            `json:"dateLastActivity"`
	Lists            []trelloList      `json:"lists"`
	Cards            []trelloCard      `json:"cards"`
	Actions          []trelloAction    `json:"actions"`
	Members          []json.RawMessage `json:"members"`
	Memberships      []json.RawMessage `json:"memberships"`
	Checklists       []json.RawMessage `json:"checklists"`
	CustomFields     []json.RawMessage `json:"customFields"`
	Plugins          []json.RawMessage `json:"plugins"`
	PowerUps         []json.RawMessage `json:"powerUps"`
}

type trelloList struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Closed bool   `json:"closed"`
}

type trelloCard struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Desc             string          `json:"desc"`
	IDList           string          `json:"idList"`
	Due              *string         `json:"due"`
	DateLastActivity string          `json:"dateLastActivity"`
	Closed           bool            `json:"closed"`
	Pos              json.RawMessage `json:"pos"`
	IDLabels         []string        `json:"idLabels"`
	DueComplete      bool            `json:"dueComplete"`
	Labels           []trelloLabel   `json:"labels"`
	IDMembers        []string        `json:"idMembers"`
	Badges           json.RawMessage `json:"badges"`
	Attachments      json.RawMessage `json:"attachments"`
	Checklists       json.RawMessage `json:"checklists"`
	CustomFields     json.RawMessage `json:"customFieldItems"`
	PluginData       json.RawMessage `json:"pluginData"`
	Stickers         json.RawMessage `json:"stickers"`
	Cover            json.RawMessage `json:"cover"`
}

type trelloLabel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type trelloAction struct {
	ID            string           `json:"id"`
	Type          string           `json:"type"`
	Date          string           `json:"date"`
	Data          trelloActionData `json:"data"`
	MemberCreator trelloMember     `json:"memberCreator"`
}

type trelloActionData struct {
	Text string        `json:"text"`
	Card trelloCardRef `json:"card"`
}

type trelloCardRef struct {
	ID string `json:"id"`
}

type trelloMember struct {
	ID       string `json:"id"`
	FullName string `json:"fullName"`
	Username string `json:"username"`
}

var trelloSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

const trelloFallbackTime = "1970-01-01T00:00:00Z"

func trelloID(entity, source string) string {
	sum := sha256.Sum256([]byte("trello\x00" + entity + "\x00" + source))
	return hex.EncodeToString(sum[:16])
}

func trelloTimestamp(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fallback
	}
	return timestamp.UTC().Format(time.RFC3339Nano)
}

func trelloSlug(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = trelloSlugPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return fallback
	}
	if len(value) > 64 {
		value = strings.Trim(value[:64], "-")
	}
	return value
}

func trelloKey(value string) string {
	var b strings.Builder
	for _, char := range strings.ToUpper(value) {
		if (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			b.WriteRune(char)
		}
		if b.Len() == 16 {
			break
		}
	}
	key := b.String()
	if key == "" {
		key = "TRELLO"
	}
	if key[0] >= '0' && key[0] <= '9' {
		key = "T" + key
	}
	if len(key) > 16 {
		key = key[:16]
	}
	return key
}

func trelloColor(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "red":
		return "#ef4444"
	case "orange":
		return "#f97316"
	case "yellow":
		return "#eab308"
	case "green":
		return "#22c55e"
	case "blue":
		return "#3b82f6"
	case "purple":
		return "#a855f7"
	case "pink":
		return "#ec4899"
	case "sky":
		return "#0ea5e9"
	case "lime":
		return "#84cc16"
	case "black":
		return "#111827"
	default:
		return "#94a3b8"
	}
}

func trelloState(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.Contains(name, "done"), strings.Contains(name, "complete"), strings.Contains(name, "closed"), strings.Contains(name, "archive"):
		return "completed"
	case strings.Contains(name, "block"):
		return "blocked"
	case strings.Contains(name, "progress"), strings.Contains(name, "doing"), strings.Contains(name, "active"):
		return "active"
	case strings.Contains(name, "ready"), strings.Contains(name, "next"):
		return "ready"
	default:
		return "backlog"
	}
}

func appendTrelloWarning(warnings *[]string, seen map[string]struct{}, message string) {
	if _, ok := seen[message]; ok {
		return
	}
	seen[message] = struct{}{}
	*warnings = append(*warnings, message)
}

func hasTrelloField(object map[string]json.RawMessage, field string) bool {
	_, ok := object[field]
	return ok
}

func trelloRawObjects(value json.RawMessage) []map[string]json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(value, &objects); err != nil {
		return nil
	}
	return objects
}

func trelloKnownFields(fields ...string) map[string]struct{} {
	known := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		known[field] = struct{}{}
	}
	return known
}

func warnTrelloUnknownFields(warnings *[]string, seen map[string]struct{}, scope string, object map[string]json.RawMessage, known map[string]struct{}) {
	unknown := make([]string, 0)
	for field := range object {
		if _, ok := known[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	sort.Strings(unknown)
	for _, field := range unknown {
		appendTrelloWarning(warnings, seen, fmt.Sprintf("Trello %s field %q is unsupported and was not imported", scope, field))
	}
}

func warnTrelloUnsupportedField(warnings *[]string, seen map[string]struct{}, scope, field string, present bool) {
	if !present {
		return
	}
	appendTrelloWarning(warnings, seen, fmt.Sprintf("Trello %s field %q is unsupported and was not imported", scope, field))
}

// ConvertTrello translates a Trello board JSON export/API response into a
// single Helm project. Supported fields are lists, cards, labels, due dates,
// and comment actions. Unsupported data is never silently discarded: callers
// receive explicit warnings suitable for an import preview.
func ConvertTrello(data []byte) (store.PortableArchive, []string, error) {
	var board trelloBoard
	if err := json.Unmarshal(data, &board); err != nil {
		return store.PortableArchive{}, nil, fmt.Errorf("invalid Trello JSON: %w", err)
	}
	var rawBoard map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawBoard); err != nil || rawBoard == nil {
		return store.PortableArchive{}, nil, fmt.Errorf("invalid Trello JSON: expected an object")
	}
	if strings.TrimSpace(board.ID) == "" {
		return store.PortableArchive{}, nil, fmt.Errorf("Trello board id is required")
	}
	if strings.TrimSpace(board.Name) == "" {
		return store.PortableArchive{}, nil, fmt.Errorf("Trello board name is required")
	}
	warnings, seenWarnings := []string{}, map[string]struct{}{}
	warnTrelloUnknownFields(&warnings, seenWarnings, "board", rawBoard, trelloKnownFields(
		"id", "name", "desc", "closed", "prefs", "preferences", "dateLastActivity", "lists", "cards", "actions",
		"members", "memberships", "checklists", "customFields", "plugins", "powerUps",
	))
	warnTrelloUnsupportedField(&warnings, seenWarnings, "board", "closed", hasTrelloField(rawBoard, "closed"))
	warnTrelloUnsupportedField(&warnings, seenWarnings, "board", "prefs", hasTrelloField(rawBoard, "prefs"))
	warnTrelloUnsupportedField(&warnings, seenWarnings, "board", "preferences", hasTrelloField(rawBoard, "preferences"))
	if hasTrelloField(rawBoard, "members") || len(board.Members) > 0 {
		appendTrelloWarning(&warnings, seenWarnings, "Trello members are unsupported; assignees were not imported")
	}
	if hasTrelloField(rawBoard, "memberships") || len(board.Memberships) > 0 {
		appendTrelloWarning(&warnings, seenWarnings, "Trello memberships and permissions are unsupported")
	}
	if hasTrelloField(rawBoard, "checklists") || len(board.Checklists) > 0 {
		appendTrelloWarning(&warnings, seenWarnings, "Trello checklists are unsupported")
	}
	if hasTrelloField(rawBoard, "customFields") || len(board.CustomFields) > 0 {
		appendTrelloWarning(&warnings, seenWarnings, "Trello custom fields are unsupported")
	}
	if hasTrelloField(rawBoard, "plugins") || hasTrelloField(rawBoard, "powerUps") || len(board.Plugins) > 0 || len(board.PowerUps) > 0 {
		appendTrelloWarning(&warnings, seenWarnings, "Trello plugins and Power-Ups are unsupported")
	}
	boardTime := trelloTimestamp(board.DateLastActivity, trelloFallbackTime)
	projectID := trelloID("project", board.ID)
	key := trelloKey(board.Name)
	slug := trelloSlug(board.Name, "trello-"+trelloID("slug", board.ID)[:8])
	archive := store.PortableArchive{
		Format: store.PortableFormat, Version: store.PortableVersion, ExportedAt: boardTime,
		Source:   store.PortableSource{Product: "trello", API: "trello-board-adapter-v1"},
		Projects: []store.PortableProject{{ID: projectID, Key: key, Slug: slug, Name: board.Name, Description: board.Desc, Color: "#64748b", Favorite: false, CreatedAt: boardTime, UpdatedAt: boardTime}},
		Columns:  []store.PortableColumn{}, Tasks: []store.PortableTask{}, Labels: []store.PortableLabel{}, Actors: []store.PortableActorReference{}, Comments: []store.PortableComment{},
		Relationships: store.PortableRelationships{TaskLabels: []store.PortableTaskLabel{}, Dependencies: []store.PortableDependency{}, TaskLinks: []store.PortableTaskLink{}},
		Activity:      store.PortableActivity{Events: []store.PortableEvent{}, AgentWork: []store.PortableAgentWork{}, AgentWorkHistory: []store.PortableAgentWorkHistory{}},
	}
	columnMap := map[string]string{}
	columnStateMap := map[string]string{}
	for index, object := range trelloRawObjects(rawBoard["lists"]) {
		warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("list %d", index), object, trelloKnownFields("id", "name", "closed"))
		warnTrelloUnsupportedField(&warnings, seenWarnings, fmt.Sprintf("list %d", index), "closed", hasTrelloField(object, "closed"))
	}
	for position, list := range board.Lists {
		if strings.TrimSpace(list.ID) == "" || strings.TrimSpace(list.Name) == "" {
			appendTrelloWarning(&warnings, seenWarnings, "Trello lists without an id or name were skipped")
			continue
		}
		columnID := trelloID("column", list.ID)
		columnMap[list.ID] = columnID
		semanticState := trelloState(list.Name)
		columnStateMap[columnID] = semanticState
		archive.Columns = append(archive.Columns, store.PortableColumn{ID: columnID, ProjectID: projectID, Name: list.Name, SemanticState: semanticState, Position: position, CreatedAt: boardTime, UpdatedAt: boardTime})
	}
	if len(archive.Columns) == 0 {
		columnID := trelloID("column", board.ID+":backlog")
		columnMap[""] = columnID
		columnStateMap[columnID] = "backlog"
		archive.Columns = append(archive.Columns, store.PortableColumn{ID: columnID, ProjectID: projectID, Name: "Backlog", SemanticState: "backlog", Position: 0, CreatedAt: boardTime, UpdatedAt: boardTime})
		appendTrelloWarning(&warnings, seenWarnings, "Trello board had no usable lists; a Backlog column was created")
	}

	labelMap := map[string]string{}
	labelOrder := []string{}
	for _, card := range board.Cards {
		for _, label := range card.Labels {
			if strings.TrimSpace(label.ID) == "" {
				continue
			}
			if _, exists := labelMap[label.ID]; exists {
				continue
			}
			labelMap[label.ID] = trelloID("label", label.ID)
			labelOrder = append(labelOrder, label.ID)
		}
	}
	sort.Strings(labelOrder)
	for _, labelID := range labelOrder {
		var label trelloLabel
		for _, card := range board.Cards {
			for _, candidate := range card.Labels {
				if candidate.ID == labelID {
					label = candidate
					break
				}
			}
			if label.ID != "" {
				break
			}
		}
		name := strings.TrimSpace(label.Name)
		if name == "" {
			name = "Trello label " + labelID
		}
		archive.Labels = append(archive.Labels, store.PortableLabel{ID: labelMap[labelID], ProjectID: projectID, Name: name, Color: trelloColor(label.Color), CreatedAt: boardTime, UpdatedAt: boardTime})
	}

	rawCards := trelloRawObjects(rawBoard["cards"])
	for cardIndex, cardObject := range rawCards {
		for labelIndex, labelObject := range trelloRawObjects(cardObject["labels"]) {
			warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("card %d label %d", cardIndex, labelIndex), labelObject, trelloKnownFields("id", "name", "color"))
		}
	}
	for number, card := range board.Cards {
		if number < len(rawCards) {
			object := rawCards[number]
			warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("card %d", number), object, trelloKnownFields(
				"id", "name", "desc", "idList", "due", "dateLastActivity", "closed", "pos", "idLabels", "dueComplete", "labels", "idMembers", "badges", "attachments", "checklists", "customFieldItems", "pluginData", "stickers", "cover",
			))
			for _, field := range []string{"closed", "pos", "idLabels", "dueComplete"} {
				warnTrelloUnsupportedField(&warnings, seenWarnings, fmt.Sprintf("card %d", number), field, hasTrelloField(object, field))
			}
		}
		if strings.TrimSpace(card.ID) == "" || strings.TrimSpace(card.Name) == "" {
			appendTrelloWarning(&warnings, seenWarnings, "Trello cards without an id or name were skipped")
			continue
		}
		columnID := columnMap[card.IDList]
		if columnID == "" {
			columnID = columnMap[""]
		}
		if columnID == "" {
			columnID = archive.Columns[0].ID
		}
		cardTime := trelloTimestamp(card.DateLastActivity, boardTime)
		var dueAt *string
		if card.Due != nil && strings.TrimSpace(*card.Due) != "" {
			parsed := trelloTimestamp(*card.Due, "")
			if parsed == "" {
				appendTrelloWarning(&warnings, seenWarnings, "one or more Trello due dates were invalid and omitted")
			} else {
				dueAt = &parsed
			}
		}
		var completedAt *string
		if columnStateMap[columnID] == "completed" {
			completedAt = &cardTime
		}
		task := store.PortableTask{ID: trelloID("task", card.ID), Number: number + 1, ProjectID: projectID, Kind: "task", ColumnID: columnID, Title: card.Name, Description: card.Desc, Priority: "normal", Position: float64(number), DueAt: dueAt, Version: 1, CompletedAt: completedAt, CreatedAt: cardTime, UpdatedAt: cardTime}
		archive.Tasks = append(archive.Tasks, task)
		for _, label := range card.Labels {
			if mapped := labelMap[label.ID]; mapped != "" {
				archive.Relationships.TaskLabels = append(archive.Relationships.TaskLabels, store.PortableTaskLabel{TaskID: task.ID, LabelID: mapped})
			}
		}
		if len(card.IDMembers) > 0 {
			appendTrelloWarning(&warnings, seenWarnings, "Trello card members are unsupported; assignees were not imported")
		}
		if number < len(rawCards) {
			object := rawCards[number]
			if hasTrelloField(object, "idMembers") {
				appendTrelloWarning(&warnings, seenWarnings, "Trello card members are unsupported; assignees were not imported")
			}
			for _, field := range []string{"badges", "attachments", "checklists", "customFieldItems", "pluginData", "stickers", "cover"} {
				warnTrelloUnsupportedField(&warnings, seenWarnings, fmt.Sprintf("card %d", number), field, hasTrelloField(object, field))
			}
		}
		if len(card.Badges) > 0 || len(card.Attachments) > 0 || len(card.Checklists) > 0 || len(card.CustomFields) > 0 || len(card.PluginData) > 0 || len(card.Stickers) > 0 || len(card.Cover) > 0 {
			appendTrelloWarning(&warnings, seenWarnings, "Trello card attachments, checklists, badges, custom fields, covers, stickers, and plugin data are unsupported")
		}
	}

	actorNames := map[string]string{}
	rawActions := trelloRawObjects(rawBoard["actions"])
	for index, actionObject := range rawActions {
		warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("action %d", index), actionObject, trelloKnownFields("id", "type", "date", "data", "memberCreator"))
		if dataObject, ok := actionObject["data"]; ok {
			var dataFields map[string]json.RawMessage
			if json.Unmarshal(dataObject, &dataFields) == nil && dataFields != nil {
				warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("action %d data", index), dataFields, trelloKnownFields("text", "card"))
				if cardObject, ok := dataFields["card"]; ok {
					var cardFields map[string]json.RawMessage
					if json.Unmarshal(cardObject, &cardFields) == nil && cardFields != nil {
						warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("action %d data card", index), cardFields, trelloKnownFields("id"))
					}
				}
			}
		}
		if memberObject, ok := actionObject["memberCreator"]; ok {
			var memberFields map[string]json.RawMessage
			if json.Unmarshal(memberObject, &memberFields) == nil && memberFields != nil {
				warnTrelloUnknownFields(&warnings, seenWarnings, fmt.Sprintf("action %d memberCreator", index), memberFields, trelloKnownFields("id", "fullName", "username"))
			}
		}
	}
	for _, action := range board.Actions {
		if action.Type != "commentCard" {
			appendTrelloWarning(&warnings, seenWarnings, "Trello non-comment actions are unsupported and were not imported")
			continue
		}
		if strings.TrimSpace(action.ID) == "" || strings.TrimSpace(action.Data.Card.ID) == "" || strings.TrimSpace(action.Data.Text) == "" {
			appendTrelloWarning(&warnings, seenWarnings, "Trello comments missing an id, card, or text were skipped")
			continue
		}
		var taskID string
		for _, task := range archive.Tasks {
			if task.ID == trelloID("task", action.Data.Card.ID) {
				taskID = task.ID
				break
			}
		}
		if taskID == "" {
			appendTrelloWarning(&warnings, seenWarnings, "Trello comments for skipped cards were skipped")
			continue
		}
		actorID := trelloID("actor", action.MemberCreator.ID)
		if strings.TrimSpace(action.MemberCreator.ID) == "" {
			actorID = trelloID("actor", "unknown")
		}
		name := strings.TrimSpace(action.MemberCreator.FullName)
		if name == "" {
			name = strings.TrimSpace(action.MemberCreator.Username)
		}
		if name == "" {
			name = "Trello member " + action.MemberCreator.ID
		}
		actorNames[actorID] = name
		createdAt := trelloTimestamp(action.Date, boardTime)
		archive.Comments = append(archive.Comments, store.PortableComment{ID: trelloID("comment", action.ID), TaskID: taskID, ActorID: actorID, Body: action.Data.Text, CreatedAt: createdAt, UpdatedAt: createdAt})
	}
	actorIDs := make([]string, 0, len(actorNames))
	for actorID := range actorNames {
		actorIDs = append(actorIDs, actorID)
	}
	sort.Strings(actorIDs)
	for _, actorID := range actorIDs {
		archive.Actors = append(archive.Actors, store.PortableActorReference{ID: actorID, Kind: "human", Name: actorNames[actorID]})
	}
	return archive, warnings, nil
}
