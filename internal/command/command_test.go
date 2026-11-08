package command

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var (
	tf      = Tools{Known: true, Engine: "terraform"}
	tofu    = Tools{Known: true, Engine: "opentofu"}
	tg      = Tools{Known: true, Engine: "terraform", Terragrunt: true}
	unknown = Tools{}
)

// CAC2: what is refused, with the reason the user reads.
func TestRefused(t *testing.T) {
	cases := []struct {
		text   string
		tools  Tools
		access Access
		want   string
	}{
		{"ssh host", tf, AccessWrite, "ssh isn’t allowed"},
		{"telnet host 23", tf, AccessWrite, "telnet isn’t allowed"},
		{"nc -z host 22", tf, AccessWrite, "nc isn’t allowed"},
		{"curl https://example.com", tf, AccessWrite, "curl isn’t allowed"},
		{"wget x", tf, AccessWrite, "wget isn’t allowed"},
		{"rm -rf /", tf, AccessWrite, "rm isn’t allowed"},
		{"sh -c 'terraform state list'", tf, AccessWrite, "sh isn’t allowed"},
		{"python3 -c 'import os'", tf, AccessWrite, "python3 isn’t allowed"},
		{"git status", tf, AccessWrite, "git isn’t allowed"},
		{"/usr/bin/terraform state list", tf, AccessWrite, "not a path"},
		{"./terraform state list", tf, AccessWrite, "not a path"},
		{"terraform state list | grep x", tf, AccessWrite, "“|” isn’t allowed"},
		{"terraform state list; ssh x", tf, AccessWrite, "“;” isn’t allowed"},
		{"terraform state list && ssh x", tf, AccessWrite, "“&” isn’t allowed"},
		{"terraform output $(id)", tf, AccessWrite, "“$” isn’t allowed"},
		{"terraform output `id`", tf, AccessWrite, "“`” isn’t allowed"},
		{"terraform state list > /tmp/x", tf, AccessWrite, "“>” isn’t allowed"},
		{"terraform state list aws_*", tf, AccessWrite, "“*” isn’t allowed"},
		{"terraform state list ~/x", tf, AccessWrite, "“~” isn’t allowed"},
		{`terraform state rm a\ b`, tf, AccessWrite, "isn’t allowed outside quotes"},
		{"terraform state show 'abc", tf, AccessWrite, "isn’t closed"},
		{"terraform apply -auto-approve", tf, AccessWrite, "use Plan & apply"},
		{"terraform destroy", tf, AccessWrite, "use Destroy"},
		{"terraform plan", tf, AccessWrite, "use Plan only"},
		{"terraform refresh", tf, AccessWrite, "isn’t allowed here"},
		{"terraform init", tf, AccessWrite, "already ran it"},
		{"terraform console", tf, AccessWrite, "console isn’t on the list"},
		{"terraform state pull", tf, AccessWrite, "secrets included"},
		{"terraform state push x.tfstate", tf, AccessWrite, "replaces the state"},
		{"terraform workspace new x", tf, AccessWrite, "workspace new isn’t on the list"},
		{"terraform providers mirror /tmp", tf, AccessWrite, "takes no arguments"},
		{"terraform output -json", tf, AccessRead, "-json would show sensitive values"},
		{"terraform output -raw db_password", tf, AccessRead, "-raw would show sensitive values"},
		{"terraform show -json", tf, AccessRead, "-json would show"},
		{"terraform show /etc/passwd", tf, AccessRead, "takes no arguments"},
		{"terraform -chdir=../../other state list", tf, AccessRead, "must stay inside the stack"},
		{"terraform -chdir=/etc state list", tf, AccessRead, "must stay inside the stack"},
		{"terraform -chdir=units/../../x state list", tf, AccessRead, "must stay inside the stack"},
		{"terraform state list -state=/tmp/x", tf, AccessRead, "-state isn’t allowed"},
		{"terraform state mv -state-out=x a b", tf, AccessWrite, "-state-out isn’t allowed"},
		{"terraform state rm -backup=x a", tf, AccessWrite, "-backup isn’t allowed"},
		{"terraform import -config=/x a b", tf, AccessWrite, "-config isn’t allowed"},
		{"terraform import -lock=false a b", tf, AccessWrite, "-lock=false isn’t allowed"},
		{"terraform import -var-file=/etc/passwd a b", tf, AccessWrite, "must stay inside the stack"},
		{"terraform state list -chdir=x", tf, AccessRead, "-chdir goes before the subcommand"},
		{"terraform -var=x state list", tf, AccessRead, "isn’t allowed before the subcommand"},
		{"terraform", tf, AccessRead, "add a subcommand"},
		{"terraform state", tf, AccessRead, "add what to do with state"},
		{"terraform state mv a", tf, AccessWrite, "needs 2 arguments"},
		{"terraform state rm a", tf, AccessRead, "Read only: choose “May change the state” to run state rm"},
		{"terraform import a b", tf, AccessRead, "Read only"},
		{"terraform force-unlock 123", tf, AccessRead, "Read only"},
		{"tofu state list", tf, AccessRead, "this stack uses Terraform: start with terraform"},
		{"terraform state list", tofu, AccessRead, "this stack uses OpenTofu: start with tofu"},
		{"terragrunt state list", tf, AccessRead, "doesn’t use Terragrunt"},
		{"terragrunt run --all -- apply", tg, AccessWrite, "use Plan & apply"},
		{"terragrunt run --all -- state pull", tg, AccessWrite, "secrets included"},
		{"terragrunt run --all state list", tg, AccessRead, "with the subcommand after --"},
		{"terragrunt run --all -- -chdir=x state list", tg, AccessRead, "isn’t allowed before the subcommand"},
		{"terragrunt --working-dir ../x state list", tg, AccessRead, "must stay inside the stack"},
		{"terragrunt --terragrunt-config x state list", tg, AccessRead, "--terragrunt-config isn’t allowed with terragrunt"},
		{"terragrunt run --all -- state rm a", tg, AccessRead, "Read only"},
		// In a Terragrunt stack the engine alone finds no unit's state.
		{"terraform -chdir=net state list", tg, AccessRead, "this stack uses Terragrunt"},
		{"terraform -chdir=net import random_id.a abc", tg, AccessWrite, "start with terragrunt"},
		{"", tf, AccessRead, "Type a command"},
		{"# only a comment\n\n", tf, AccessRead, "Type a command"},
		{strings.Repeat("x", MaxLength+1), tf, AccessRead, "too long"},
	}
	for _, c := range cases {
		_, err := Check(c.text, c.tools, c.access)
		if err == nil {
			t.Errorf("%q: accepted, want %q", c.text, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %q, want it to contain %q", c.text, err, c.want)
		}
	}
}

// CAC2: what is accepted, and how it is split into arguments.
func TestAccepted(t *testing.T) {
	cases := []struct {
		text   string
		tools  Tools
		access Access
		args   [][]string
		writes bool
	}{
		{"terraform state list", tf, AccessRead, [][]string{{"terraform", "state", "list"}}, false},
		{`terraform state mv 'module.a.x["k"]' module.b.x`, tf, AccessWrite,
			[][]string{{"terraform", "state", "mv", `module.a.x["k"]`, "module.b.x"}}, true},
		{`terraform state rm "aws_instance.web[\"a\"]"`, tf, AccessWrite,
			[][]string{{"terraform", "state", "rm", `aws_instance.web["a"]`}}, true},
		{"terraform state show aws_instance.web[0]", tf, AccessRead,
			[][]string{{"terraform", "state", "show", "aws_instance.web[0]"}}, false},
		{"terraform -chdir=units/vpc state list", tf, AccessRead, [][]string{{"terraform", "-chdir=units/vpc", "state", "list"}}, false},
		{"terragrunt run --all -- state list", tg, AccessRead, [][]string{{"terragrunt", "run", "--all", "--", "state", "list"}}, false},
		{"terragrunt --working-dir units/vpc state list", tg, AccessRead, [][]string{{"terragrunt", "--working-dir", "units/vpc", "state", "list"}}, false},
		{"tofu output", tofu, AccessRead, [][]string{{"tofu", "output"}}, false},
		{"tofu output db_host", unknown, AccessRead, [][]string{{"tofu", "output", "db_host"}}, false},
		{"terraform import -var-file=prod.tfvars aws_s3_bucket.logs acme-logs", tf, AccessWrite,
			[][]string{{"terraform", "import", "-var-file=prod.tfvars", "aws_s3_bucket.logs", "acme-logs"}}, true},
		{"terraform force-unlock -force 6f1c-22", tf, AccessWrite, [][]string{{"terraform", "force-unlock", "-force", "6f1c-22"}}, true},
		{"terraform state mv \\\n  module.old.x \\\n  module.new.x", tf, AccessWrite,
			[][]string{{"terraform", "state", "mv", "module.old.x", "module.new.x"}}, true},
		{"# list first\nterraform state list\n\nterraform validate # then check", tf, AccessRead,
			[][]string{{"terraform", "state", "list"}, {"terraform", "validate"}}, false},
		{"terraform version", tf, AccessRead, [][]string{{"terraform", "version"}}, false},
		{"terraform workspace show", tf, AccessRead, [][]string{{"terraform", "workspace", "show"}}, false},
		{"terraform output -no-color", tf, AccessRead, [][]string{{"terraform", "output", "-no-color"}}, false},
		{"terraform state list -lock=true", tf, AccessRead, [][]string{{"terraform", "state", "list", "-lock=true"}}, false},
	}
	for _, c := range cases {
		lines, err := Check(c.text, c.tools, c.access)
		if err != nil {
			t.Errorf("%q: %v", c.text, err)
			continue
		}
		var got [][]string
		writes := false
		for _, l := range lines {
			got = append(got, l.Args)
			writes = writes || l.Writes
		}
		if !reflect.DeepEqual(got, c.args) {
			t.Errorf("%q: args %q, want %q", c.text, got, c.args)
		}
		if writes != c.writes {
			t.Errorf("%q: writes %v, want %v", c.text, writes, c.writes)
		}
	}
}

func TestLineNumbersAndDirs(t *testing.T) {
	text := "# move it\nterraform state mv \\\n  a b\n\nterraform -chdir=units/x state list"
	lines, err := Check(text, tf, AccessWrite)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Number != 2 || lines[1].Number != 5 {
		t.Fatalf("lines %+v", lines)
	}
	if !reflect.DeepEqual(lines[1].Dirs, []string{"units/x"}) || lines[0].Subcommand != "state mv" {
		t.Fatalf("lines %+v", lines)
	}
	_, err = Check("terraform state list\nssh x", tf, AccessRead)
	var ce *Error
	if !errors.As(err, &ce) || ce.Line != 2 {
		t.Fatalf("error %v, want one on line 2", err)
	}
}
