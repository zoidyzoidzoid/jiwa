package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/andygrunwald/go-jira"
	"github.com/catouc/jiwa/internal/editor"
	"github.com/catouc/jiwa/internal/jiwa"
	"net/http"
	"os"
	"path"
	"strings"
	"text/tabwriter"
	"time"
)

var (
	create   = flag.NewFlagSet("create", flag.ContinueOnError)
	edit     = flag.NewFlagSet("edit", flag.ContinueOnError)
	list     = flag.NewFlagSet("list", flag.ContinueOnError)
	move     = flag.NewFlagSet("move", flag.ContinueOnError)
	reassign = flag.NewFlagSet("reassign", flag.ContinueOnError)
	label    = flag.NewFlagSet("label", flag.ContinueOnError)

	createProject = create.String("project", "", "Set the project to create the ticket in, if not set it will default to your configured \"defaultProject\"")
	createIn      = create.String("in", "", "Control from where the ticket is filled in, can be a file path or \"-\" for stdin")

	listUser    = list.String("user", "", "Set the user name to use in the list call, use \"empty\" to list unassigned tickets")
	listStatus  = list.String("status", "to do", "Set the status of the tickets you want to see")
	listProject = list.String("project", "", "Set the project to search in")
)

type Config struct {
	BaseURL        string `json:"baseURL"`
	APIVersion     string `json:"apiVersion"`
	EndpointPrefix string `json:"endpointPrefix"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	DefaultProject string `json:"defaultProject"`
}

var cfg Config

func init() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("cannot locate user home dir, is `$HOME` set? Detailed error: %s\n", err)
		os.Exit(1)
	}

	cfgFileLoc := path.Join(homeDir, ".config", "jiwa", "config.json")

	cfgBytes, err := os.ReadFile(cfgFileLoc)
	if err != nil {
		fmt.Printf("cannot locate configuration file, was it created under %s? Detailed error: %s\n", cfgFileLoc, err)
		os.Exit(1)
	}

	err = json.Unmarshal(cfgBytes, &cfg)
	if err != nil {
		fmt.Printf("failed to read configuration file: %s\n", err)
		os.Exit(1)
	}

	username, set := os.LookupEnv("JIWA_USERNAME")
	if set {
		cfg.Username = username
	}
	password, set := os.LookupEnv("JIWA_PASSWORD")
	if set {
		cfg.Password = password
	}

	if cfg.Password == "" || cfg.Username == "" || cfg.BaseURL == "" {
		fmt.Printf(`Config is missing important values, \"baseURL\", \"username\" and \"password\" need to be set.
"username" and "password" can be configured through their respective environment variables "JIWA_USERNAME" and "JIWA_PASSWORD".
The configuration file is located at %s
`, cfgFileLoc)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Printf("Usage: jiwa {create|edit|list|move|reassign}\n")
		os.Exit(1)
	}

}

func main() {
	httpClient := http.DefaultClient
	httpClient.Timeout = 3 * time.Second

	c := jiwa.Client{
		Username:   cfg.Username,
		Password:   cfg.Password,
		BaseURL:    cfg.BaseURL + cfg.EndpointPrefix,
		APIVersion: cfg.APIVersion,
		HTTPClient: httpClient,
	}

	stat, _ := os.Stdin.Stat()

	switch os.Args[1] {
	case "create":
		err := create.Parse(os.Args[2:])
		if err != nil {
			fmt.Println("Usage: jiwa create [-project]")
			os.Exit(1)
		}

		var project string
		switch {
		case *createProject == "" && cfg.DefaultProject != "":
			project = cfg.DefaultProject
		case *createProject == "" && cfg.DefaultProject == "":
			fmt.Println("Usage: jiwa create [-project]")
			os.Exit(1)
		case *createProject != "":
			project = *createProject
		}

		var summary, description string
		switch *createIn {
		case "":
			summary, description, err = CreateIssueSummaryDescription("")
			if err != nil {
				fmt.Printf("failed to get summary and description: %s\n", err)
				os.Exit(1)
			}
		case "-":
			in, err := readStdin()
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}

			fmt.Println(string(in))

			scanner := bufio.NewScanner(bytes.NewBuffer(in))
			summary, description, err = BuildSummaryAndDescriptionFromScanner(scanner)
			if err != nil {
				fmt.Printf("failed to get summary and description: %s\n", err)
				os.Exit(1)
			}
		default:
			fBytes, err := os.ReadFile(*createIn)
			if err != nil {
				fmt.Printf("failed to read file contents: %s", err)
				os.Exit(1)
			}

			scanner := bufio.NewScanner(bytes.NewBuffer(fBytes))

			summary, description, err = BuildSummaryAndDescriptionFromScanner(scanner)
			if err != nil {
				fmt.Printf("failed to get summary and description: %s\n", err)
				os.Exit(1)
			}
		}

		issue, err := c.CreateIssue(context.TODO(), jiwa.CreateIssueInput{
			Project:     project,
			Summary:     summary,
			Description: description,
			Labels:      nil,
			Type:        "Task",
		})
		if err != nil {
			fmt.Printf("failed to create issue: %s\n", err)
			os.Exit(1)
		}

		fmt.Println(ConstructIssueURL(issue.Key, cfg.BaseURL))
	case "edit":
		if len(os.Args) != 3 {
			fmt.Println("Usage: jiwa edit <issue ID>")
			os.Exit(1)
		}

		summary, description, err := GetIssueIntoEditor(c, os.Args[2])
		if err != nil {
			fmt.Printf("failed to get summary and description: %s\n", err)
			os.Exit(1)
		}

		fmt.Printf("summary: %s | description: %s\n", summary, description)

		err = c.UpdateIssue(context.TODO(), jira.Issue{
			Key: os.Args[2],
			Fields: &jira.IssueFields{
				Summary:     summary,
				Description: description,
			},
		})
		if err != nil {
			fmt.Printf("failed to update issue: %s\n", err)
			os.Exit(1)
		}

		fmt.Println(ConstructIssueURL(os.Args[2], cfg.BaseURL))
	case "list":
	case "ls":
		err := list.Parse(os.Args[2:])
		if err != nil {
			fmt.Println("Usage: jiwa ls [-user|-status]")
			os.Exit(1)
		}

		var user string
		switch *listUser {
		case "empty":
			user = "AND assignee is EMPTY"
		case "":
			user = ""
		default:
			user = "AND assignee= " + *listUser
		}

		project := cfg.DefaultProject
		if *listProject != "" {
			project = *listProject
		}

		jql := fmt.Sprintf("project=%s AND status=\"%s\" %s", project, *listStatus, user)
		issues, err := c.Search(context.TODO(), jql)
		if err != nil {
			fmt.Printf("could not list issues: %s\n", err)
			os.Exit(1)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', tabwriter.AlignRight)
		fmt.Fprintf(w, "ID\tSummary\tURL\n")
		for _, i := range issues {
			issueURL := fmt.Sprintf("%s/browse/%s", c.BaseURL, i.Key)
			fmt.Fprintf(w, "%s\t%s\t%s\n", i.Key, i.Fields.Summary, issueURL)
		}
		w.Flush()
	case "move":
	case "mv":
	case "reassign":
		var ticketID, user string
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			if len(os.Args) != 3 {
				fmt.Println("Usage: jiwa reassign <username>")
				os.Exit(1)
			}

			in, err := readStdin()
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}

			ticketID = StripBaseURL(string(in), cfg.BaseURL)
			user = os.Args[2]
		} else {
			if len(os.Args) != 4 {
				fmt.Println("Usage: jiwa reassign <issue ID> <username>")
				os.Exit(1)
			}
			ticketID = os.Args[2]
			user = os.Args[3]
		}

		err := c.AssignIssue(context.TODO(), ticketID, user)
		if err != nil {
			fmt.Printf("failed to assign issue to %s: %s\n", ticketID, err)
			os.Exit(1)
		}

		fmt.Println(ConstructIssueURL(ticketID, cfg.BaseURL))
	case "label":
		var ticketID string
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			if len(os.Args) < 3 {
				fmt.Println("Usage: jiwa label <label> <label> ...")
			}

			in, err := readStdin()
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}

			ticketID = StripBaseURL(string(in), cfg.BaseURL)
		} else {
			if len(os.Args) < 4 {
				fmt.Println("Usage: jiwa label <issue ID> <label> <label>...")
				os.Exit(1)
			}
			ticketID = os.Args[2]
		}

		err := c.LabelIssue(context.TODO(), ticketID, os.Args[2:]...)
		if err != nil {
			fmt.Printf("failed to label issue: %s\n", err)
			os.Exit(1)
		}

		fmt.Println(ticketID)
		fmt.Println(ConstructIssueURL(ticketID, cfg.BaseURL))
	}
}

func CreateIssueSummaryDescription(prefill string) (string, string, error) {
	scanner, cleanup, err := editor.SetupTmpFileWithEditor(prefill)
	if err != nil {
		return "", "", fmt.Errorf("failed to set up scanner on tmpFile: %w", err)
	}
	defer cleanup()

	title, description, err := BuildSummaryAndDescriptionFromScanner(scanner)
	if err != nil {
		return "", "", fmt.Errorf("scanner failure: %w", err)
	}

	if title == "" {
		return "", "", errors.New("the summary line needs to be filled at least")
	}

	return title, description, nil
}

func BuildSummaryAndDescriptionFromScanner(scanner *bufio.Scanner) (string, string, error) {
	var title string
	descriptionBuilder := strings.Builder{}
	for scanner.Scan() {
		if title == "" {
			title = scanner.Text()
			continue
		}
		descriptionBuilder.WriteString(scanner.Text())
		descriptionBuilder.WriteString("\n")
	}

	return title, descriptionBuilder.String(), scanner.Err()
}

func GetIssueIntoEditor(c jiwa.Client, key string) (string, string, error) {
	issue, err := c.GetIssue(context.TODO(), key)
	if err != nil {
		return "", "", err
	}

	return CreateIssueSummaryDescription(issue.Fields.Summary + "\n" + issue.Fields.Description)
}

func readStdin() ([]byte, error) {
	var buf []byte
	scanner := bufio.NewScanner(os.Stdin)

	var str string
	for scanner.Scan() {
		str += scanner.Text() + "\n"
		buf = append(buf, scanner.Bytes()...)
		buf = append(buf, 10) // add the newline back into the buffer
	}

	err := scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("failed to read stdin: %v", err)
	}

	return buf, nil
}

func StripBaseURL(url, baseURL string) string {
	return strings.TrimPrefix(baseURL+"/browse/", url)
}

func ConstructIssueURL(issueKey, baseURL string) string {
	return fmt.Sprintf("%s/browse/%s", baseURL, issueKey)
}
