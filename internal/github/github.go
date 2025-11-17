package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	g4 "github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

const (
	PROJECT_ID   = "PVT_kwDOAM_34M4AAThW"
	ORGANIZATION = "kubernetes"
)

type ProjectManagerInterface interface {
	GetProjectFields() ([]ProjectFieldInfo, error)
	CreateDraftIssue(title, body, board string) error
}

// ProjectManager represents a GitHub organization with a global workflow file and reference
type ProjectManager struct {
	// organization is the GitHub organization name
	organization string

	// projectID is the ID of the Kubernetes version project board
	projectID string

	// fields is a map of project field names to their IDs
	fields map[string]ProjectFieldInfo

	// githubClient is the official GitHub API v4 (GraphQL) client
	githubClient *g4.Client
}

// ProjectFieldInfo represents a project field with its options
type ProjectFieldInfo struct {
	ID      g4.ID
	Name    g4.String
	Options map[string]interface{} // option name -> option ID
}

// NewProjectManager creates a new ProjectManager
func NewProjectManager(ctx context.Context, token string) ProjectManagerInterface {
	return &ProjectManager{
		organization: ORGANIZATION,
		projectID:    PROJECT_ID,
		fields:       map[string]ProjectFieldInfo{},
		githubClient: g4.NewClient(oauth2.NewClient(
			ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		)),
	}
}

// GetProjectFields queries the project fields and their options
func (g *ProjectManager) GetProjectFields() ([]ProjectFieldInfo, error) {
	if g.githubClient == nil {
		return nil, errors.New("github GraphQL client is nil")
	}

	var query struct {
		Node struct {
			ProjectV2 struct {
				Fields struct {
					Nodes []struct {
						Typename string `graphql:"__typename"`
						// Single select field
						ProjectV2SingleSelectField struct {
							ID      g4.ID
							Name    g4.String
							Options []struct {
								ID   g4.ID
								Name g4.String
							}
						} `graphql:"... on ProjectV2SingleSelectField"`
						// Iteration field
						ProjectV2IterationField struct {
							ID   g4.ID
							Name g4.String
						} `graphql:"... on ProjectV2IterationField"`
					}
				} `graphql:"fields(first: 50)"`
			} `graphql:"... on ProjectV2"`
		} `graphql:"node(id: $projectID)"`
	}

	variables := map[string]interface{}{
		"projectID": g4.ID(g.projectID),
	}

	if err := g.githubClient.Query(context.Background(), &query, variables); err != nil {
		return nil, fmt.Errorf("failed to query project fields: %w", err)
	}

	fields := make([]ProjectFieldInfo, 0, len(query.Node.ProjectV2.Fields.Nodes))

	for _, node := range query.Node.ProjectV2.Fields.Nodes {
		var fieldID g4.ID
		var fieldName g4.String
		options := make(map[string]interface{})

		// Handle different field types based on __typename
		switch node.Typename {
		case "ProjectV2SingleSelectField":
			fieldID = node.ProjectV2SingleSelectField.ID
			fieldName = node.ProjectV2SingleSelectField.Name
			for _, opt := range node.ProjectV2SingleSelectField.Options {
				options[string(opt.Name)] = opt.ID
			}
		case "ProjectV2IterationField":
			fieldID = node.ProjectV2IterationField.ID
			fieldName = node.ProjectV2IterationField.Name
		default:
			continue
		}

		fields = append(fields, ProjectFieldInfo{
			ID:      fieldID,
			Name:    fieldName,
			Options: options,
		})
	}

	return fields, nil
}

// CreateDraftIssue creates a new issue draft issue in the board with a
// specific test issue template.
func (g *ProjectManager) CreateDraftIssue(title, body, board string) error {
	if g.githubClient == nil {
		return errors.New("github GraphQL client is nil")
	}

	// first, get the project fields to find the correct field IDs and option IDs
	fields, err := g.GetProjectFields()
	if err != nil {
		return fmt.Errorf("failed to get project fields: %w", err)
	}

	// find the fields we need
	var k8sReleaseFieldID, viewFieldID, statusFieldID, boardFieldID g4.ID
	var k8sReleaseValueID, viewValueID, statusValueID, boardValueID g4.ID

	for _, field := range fields {
		fieldNameLower := strings.ToLower(string(field.Name))

		// find K8s Release field - look for fields containing "k8s", "release", or "version"
		if strings.Contains(fieldNameLower, "k8s release") {
			k8sReleaseFieldID = field.ID
			// find the latest version option (highest version number)
			latestVersion := ""
			latestVersionID := g4.ID("")
			for optName, optID := range field.Options {
				// extract version number from option name (e.g., "v1.32" -> "1.32")
				if version := extractVersion(optName); version != "" {
					if latestVersion == "" || compareVersions(version, latestVersion) > 0 {
						latestVersion = version
						latestVersionID = optID
					}
				}
			}
			if latestVersionID != g4.ID("") {
				k8sReleaseValueID = latestVersionID
			}
		}

		// find view field - look for fields containing "view"
		if strings.Contains(fieldNameLower, "view") {
			viewFieldID = field.ID
			// find "issue-tracking" option
			for optName, optID := range field.Options {
				if strings.Contains(strings.ToLower(optName), "issue-tracking") ||
					strings.Contains(strings.ToLower(optName), "issue tracking") {
					viewValueID = optID
					break
				}
			}
		}

		// find the board field, master-informing or master-blocking
		if strings.Contains(fieldNameLower, "board") {
			boardFieldID = field.ID
			for optName, optID := range field.Options {
				if strings.Contains(board, strings.ToLower(optName)) {
					boardValueID = optID
					break
				}
			}
		}

		// find Status field
		if strings.Contains(fieldNameLower, "status") {
			statusFieldID = field.ID
			for optName, optID := range field.Options {
				if strings.Contains(strings.ToLower(optName), "drafting") ||
					strings.Contains(strings.ToLower(optName), "draft") {
					statusValueID = optID
					break
				}
			}
		}
	}

	// create the draft issue
	var mutationDraft struct {
		AddProjectV2DraftIssue struct {
			ProjectItem struct {
				ID g4.ID
			}
		} `graphql:"addProjectV2DraftIssue(input: $input)"`
	}
	bodyInput := g4.String(body)
	inputDraft := g4.AddProjectV2DraftIssueInput{
		ProjectID: g4.ID(g.projectID),
		Title:     g4.String(title),
		Body:      &bodyInput,
	}

	if err := g.githubClient.Mutate(context.Background(), &mutationDraft, inputDraft, nil); err != nil {
		return fmt.Errorf("failed to create draft issue: %w", err)
	}

	itemID := mutationDraft.AddProjectV2DraftIssue.ProjectItem.ID
	var mutationUpdate struct {
		UpdateProjectV2ItemFieldValue struct {
			ClientMutationID string
		} `graphql:"updateProjectV2ItemFieldValue(input: $input)"`
	}

	fieldUpdates := []struct {
		fieldID   g4.ID
		optionID  g4.ID
		fieldName string
	}{
		{k8sReleaseFieldID, k8sReleaseValueID, "K8s Release"},
		{viewFieldID, viewValueID, "View"},
		{statusFieldID, statusValueID, "Status"},
		{boardFieldID, boardValueID, "Testgrid Board"},
	}

	for _, update := range fieldUpdates {
		if update.fieldID != "" && update.optionID != "" {
			optionIDStr := fmt.Sprintf("%s", update.optionID)
			if err := g.githubClient.Mutate(context.Background(), &mutationUpdate, g4.UpdateProjectV2ItemFieldValueInput{
				ProjectID: g4.ID(g.projectID),
				ItemID:    itemID,
				FieldID:   update.fieldID,
				Value:     g4.ProjectV2FieldValue{SingleSelectOptionID: (*g4.String)(&optionIDStr)},
			}, nil); err != nil {
				fmt.Printf("Warning: failed to update %s field: %v\n", update.fieldName, err)
			}
		}
	}
	return nil
}

// extractVersion extracts a version string from text (e.g., "v1.32" -> "1.32", "1.30" -> "1.30")
func extractVersion(text string) string {
	versionPattern := regexp.MustCompile(`v?(\d+)\.(\d+)`)
	if matches := versionPattern.FindStringSubmatch(text); len(matches) >= 3 {
		return fmt.Sprintf("%s.%s", matches[1], matches[2])
	}
	return ""
}

// compareVersions compares two version strings (e.g., "1.30", "1.31")
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal
func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var num1, num2 int
		if i < len(parts1) {
			num1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			num2, _ = strconv.Atoi(parts2[i])
		}

		if num1 > num2 {
			return 1
		}
		if num1 < num2 {
			return -1
		}
	}

	return 0
}
