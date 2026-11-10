package report

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
)

func init() {
	_ = Register("junit", newJUnit)
}

func newJUnit(settings map[string]any) (Reporter, error) {
	return &fileReporter{
		path:   settingPath(settings, "reports/junit.xml"),
		render: renderJUnit,
	}, nil
}

// JUnit XML shapes (minimal subset consumed by Jenkins/GitHub Actions).
type junitRoot struct {
	XMLName   xml.Name      `xml:"testsuites"`
	Name      string        `xml:"name,attr"`
	Tests     int           `xml:"tests,attr"`
	Failures  int           `xml:"failures,attr"`
	Errors    int           `xml:"errors,attr"`
	Time      string        `xml:"time,attr"`
	TestSuite junitTestSuit `xml:"testsuite"`
}

type junitTestSuit struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Errors   int             `xml:"errors,attr"`
	Time     string          `xml:"time,attr"`
	Cases    []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Classname string      `xml:"classname,attr"`
	Name      string      `xml:"name,attr"`
	Time      string      `xml:"time,attr"`
	Failure   *junitIssue `xml:"failure,omitempty"`
	Error     *junitIssue `xml:"error,omitempty"`
	SystemOut string      `xml:"system-out,omitempty"`
}

type junitIssue struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

func renderJUnit(run RunSummary) ([]byte, error) {
	var failures, errors int
	cases := make([]junitTestCase, 0, len(run.Scenarios))
	for _, s := range run.Scenarios {
		tc := junitTestCase{
			Classname: "obatala",
			Name:      s.ID,
			Time:      seconds(s.Duration),
		}
		switch s.Outcome {
		case condition.OutcomeFail:
			failures++
			tc.Failure = &junitIssue{
				Message: strings.Join(s.BudgetViolations, "; "),
				Body:    issueBody(s.Reasoning, s.UnmetCriteria, s.TriggeredFailures, s.BudgetViolations),
			}
			if tc.Failure.Message == "" {
				tc.Failure.Message = s.Reasoning
			}
		case condition.OutcomeError:
			errors++
			tc.Error = &junitIssue{Message: s.Error, Body: s.Error}
		}
		if len(s.Transcript) > 0 {
			tc.SystemOut = strings.Join(s.Transcript, "\n")
		}
		cases = append(cases, tc)
	}
	root := junitRoot{
		Name:     "obatala",
		Tests:    len(run.Scenarios),
		Failures: failures,
		Errors:   errors,
		Time:     seconds(run.Duration),
		TestSuite: junitTestSuit{
			Name:     "obatala",
			Tests:    len(run.Scenarios),
			Failures: failures,
			Errors:   errors,
			Time:     seconds(run.Duration),
			Cases:    cases,
		},
	}
	data, err := xml.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("report: marshal junit: %w", err)
	}
	return append([]byte(xml.Header), append(data, '\n')...), nil
}

func issueBody(reasoning string, unmet, triggered, violations []string) string {
	var b strings.Builder
	if reasoning != "" {
		b.WriteString(reasoning)
	}
	writeList(&b, "unmet", unmet)
	writeList(&b, "triggered", triggered)
	writeList(&b, "budget", violations)
	return b.String()
}

func writeList(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString("\n" + label + ":\n")
	for _, it := range items {
		b.WriteString("  - " + it + "\n")
	}
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.3f", d.Seconds())
}
