// Copyright 2013 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package juju

import (
	"bytes"
	"errors"
	"github.com/globocom/commandmocker"
	"github.com/globocom/config"
	"github.com/globocom/tsuru/app"
	"github.com/globocom/tsuru/provision"
	"github.com/globocom/tsuru/repository"
	"github.com/globocom/tsuru/testing"
	"labix.org/v2/mgo/bson"
	. "launchpad.net/gocheck"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (s *S) TestShouldBeRegistered(c *C) {
	p, err := provision.Get("juju")
	c.Assert(err, IsNil)
	c.Assert(p, FitsTypeOf, &JujuProvisioner{})
}

func (s *S) TestELBSupport(c *C) {
	defer config.Unset("juju:use-elb")
	config.Set("juju:use-elb", true)
	p := JujuProvisioner{}
	c.Assert(p.elbSupport(), Equals, true)
	config.Set("juju:use-elb", false)
	c.Assert(p.elbSupport(), Equals, true) // Read config only once.
	p = JujuProvisioner{}
	c.Assert(p.elbSupport(), Equals, false)
	config.Unset("juju:use-elb")
	p = JujuProvisioner{}
	c.Assert(p.elbSupport(), Equals, false)
}

func (s *S) TestUnitsCollection(c *C) {
	p := JujuProvisioner{}
	collection := p.unitsCollection()
	c.Assert(collection.Name, Equals, s.collName)
}

func (s *S) TestProvision(c *C) {
	config.Set("juju:charms-path", "/etc/juju/charms")
	defer config.Unset("juju:charms-path")
	config.Set("host", "somehost")
	defer config.Unset("host")
	tmpdir, err := commandmocker.Add("juju", "$*")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("trace", "python", 0)
	p := JujuProvisioner{}
	err = p.Provision(app)
	c.Assert(err, IsNil)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	expected := "deploy --repository /etc/juju/charms local:python trace"
	c.Assert(commandmocker.Output(tmpdir), Equals, expected)
}

func (s *S) TestProvisionUndefinedCharmsPath(c *C) {
	config.Unset("juju:charms-path")
	p := JujuProvisioner{}
	err := p.Provision(testing.NewFakeApp("eternity", "sandman", 0))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, `Setting "juju:charms-path" is not defined.`)
}

func (s *S) TestProvisionFailure(c *C) {
	config.Set("juju:charms-path", "/home/charms")
	defer config.Unset("juju:charms-path")
	tmpdir, err := commandmocker.Error("juju", "juju failed", 1)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("trace", "python", 0)
	p := JujuProvisioner{}
	err = p.Provision(app)
	c.Assert(err, NotNil)
	pErr, ok := err.(*provision.Error)
	c.Assert(ok, Equals, true)
	c.Assert(pErr.Reason, Equals, "juju failed")
	c.Assert(pErr.Err.Error(), Equals, "exit status 1")
}

func (s *S) TestDestroy(c *C) {
	tmpdir, err := commandmocker.Add("juju", "$*")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("cribcaged", "python", 3)
	p := JujuProvisioner{}
	err = p.unitsCollection().Insert(
		instance{UnitName: "cribcaged/0"},
		instance{UnitName: "cribcaged/1"},
		instance{UnitName: "cribcaged/2"},
	)
	c.Assert(err, IsNil)
	err = p.Destroy(app)
	c.Assert(err, IsNil)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	expected := []string{
		"destroy-service", "cribcaged",
		"terminate-machine", "1",
		"terminate-machine", "2",
		"terminate-machine", "3",
	}
	ran := make(chan bool, 1)
	go func() {
		for {
			if reflect.DeepEqual(commandmocker.Parameters(tmpdir), expected) {
				ran <- true
			}
		}
	}()
	n, err := p.unitsCollection().Find(bson.M{
		"_id": bson.M{
			"$in": []string{"cribcaged/0", "cribcaged/1", "cribcaged/2"},
		},
	}).Count()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, 0)
	select {
	case <-ran:
	case <-time.After(2e9):
		c.Errorf("Did not run terminate-machine commands after 2 seconds.")
	}
	c.Assert(commandmocker.Parameters(tmpdir), DeepEquals, expected)
}

func (s *S) TestDestroyFailure(c *C) {
	tmpdir, err := commandmocker.Error("juju", "juju failed to destroy the machine", 25)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("idioglossia", "static", 1)
	p := JujuProvisioner{}
	err = p.Destroy(app)
	c.Assert(err, NotNil)
	pErr, ok := err.(*provision.Error)
	c.Assert(ok, Equals, true)
	c.Assert(pErr.Reason, Equals, "juju failed to destroy the machine")
	c.Assert(pErr.Err.Error(), Equals, "exit status 25")
}

func (s *S) TestAddUnits(c *C) {
	tmpdir, err := commandmocker.Add("juju", addUnitsOutput)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("resist", "rush", 0)
	p := JujuProvisioner{}
	units, err := p.AddUnits(app, 4)
	c.Assert(err, IsNil)
	c.Assert(units, HasLen, 4)
	names := make([]string, len(units))
	for i, unit := range units {
		names[i] = unit.Name
	}
	expected := []string{"resist/3", "resist/4", "resist/5", "resist/6"}
	c.Assert(names, DeepEquals, expected)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	expectedParams := []string{
		"set", "resist", "app-repo=" + repository.GetReadOnlyUrl("resist"),
		"add-unit", "resist", "--num-units", "4",
	}
	c.Assert(commandmocker.Parameters(tmpdir), DeepEquals, expectedParams)
	_, err = getQueue(queueName).Get(1e6)
	c.Assert(err, NotNil)
}

func (s *S) TestAddZeroUnits(c *C) {
	p := JujuProvisioner{}
	units, err := p.AddUnits(nil, 0)
	c.Assert(units, IsNil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "Cannot add zero units.")
}

func (s *S) TestAddUnitsFailure(c *C) {
	tmpdir, err := commandmocker.Error("juju", "juju failed", 1)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("headlong", "rush", 1)
	p := JujuProvisioner{}
	units, err := p.AddUnits(app, 1)
	c.Assert(units, IsNil)
	c.Assert(err, NotNil)
	e, ok := err.(*provision.Error)
	c.Assert(ok, Equals, true)
	c.Assert(e.Reason, Equals, "juju failed")
	c.Assert(e.Err.Error(), Equals, "exit status 1")
}

func (s *S) TestRemoveUnit(c *C) {
	tmpdir, err := commandmocker.Add("juju", "removed")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("two", "rush", 3)
	p := JujuProvisioner{}
	err = p.unitsCollection().Insert(instance{UnitName: "two/2", InstanceId: "i-00000439"})
	c.Assert(err, IsNil)
	err = p.RemoveUnit(app, "two/2")
	c.Assert(err, IsNil)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	expected := []string{"remove-unit", "two/2", "terminate-machine", "3"}
	ran := make(chan bool, 1)
	go func() {
		for {
			if reflect.DeepEqual(commandmocker.Parameters(tmpdir), expected) {
				ran <- true
			}
		}
	}()
	n, err := p.unitsCollection().Find(bson.M{"_id": "two/2"}).Count()
	c.Assert(err, IsNil)
	c.Assert(n, Equals, 0)
	select {
	case <-ran:
	case <-time.After(2e9):
		c.Errorf("Did not run terminate-machine command after 2 seconds.")
	}
}

func (s *S) TestRemoveUnitUnknownByJuju(c *C) {
	output := `013-01-11 20:02:07,883 INFO Connecting to environment...
2013-01-11 20:02:10,147 INFO Connected to environment.
2013-01-11 20:02:10,160 ERROR Service unit 'two/2' was not found`
	tmpdir, err := commandmocker.Error("juju", output, 1)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("two", "rush", 3)
	p := JujuProvisioner{}
	err = p.RemoveUnit(app, "two/2")
	c.Assert(err, IsNil)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
}

func (s *S) TestRemoveUnknownUnit(c *C) {
	app := testing.NewFakeApp("tears", "rush", 2)
	p := JujuProvisioner{}
	err := p.RemoveUnit(app, "tears/2")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, `App "tears" does not have a unit named "tears/2".`)
}

func (s *S) TestRemoveUnitFailure(c *C) {
	tmpdir, err := commandmocker.Error("juju", "juju failed", 66)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("something", "rush", 1)
	p := JujuProvisioner{}
	err = p.RemoveUnit(app, "something/0")
	c.Assert(err, NotNil)
	e, ok := err.(*provision.Error)
	c.Assert(ok, Equals, true)
	c.Assert(e.Reason, Equals, "juju failed")
	c.Assert(e.Err.Error(), Equals, "exit status 66")
}

func (s *S) TestExecuteCommand(c *C) {
	var buf bytes.Buffer
	tmpdir, err := commandmocker.Add("juju", "$*")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("almah", "static", 2)
	p := JujuProvisioner{}
	err = p.ExecuteCommand(&buf, &buf, app, "ls", "-lh")
	c.Assert(err, IsNil)
	bufOutput := `Output from unit "almah/0":

ssh -o StrictHostKeyChecking no -q 1 ls -lh

Output from unit "almah/1":

ssh -o StrictHostKeyChecking no -q 2 ls -lh
`
	cmdOutput := "ssh -o StrictHostKeyChecking no -q 1 ls -lhssh -o StrictHostKeyChecking no -q 2 ls -lh"
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	c.Assert(commandmocker.Output(tmpdir), Equals, cmdOutput)
	c.Assert(buf.String(), Equals, bufOutput)
}

func (s *S) TestExecuteCommandFailure(c *C) {
	var buf bytes.Buffer
	tmpdir, err := commandmocker.Error("juju", "failed", 2)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("frases", "static", 1)
	p := JujuProvisioner{}
	err = p.ExecuteCommand(&buf, &buf, app, "ls", "-l")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "exit status 2")
	c.Assert(buf.String(), Equals, "failed\n")
}

func (s *S) TestExecuteCommandOneUnit(c *C) {
	var buf bytes.Buffer
	tmpdir, err := commandmocker.Add("juju", "$*")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("almah", "static", 1)
	p := JujuProvisioner{}
	err = p.ExecuteCommand(&buf, &buf, app, "ls", "-lh")
	c.Assert(err, IsNil)
	output := "ssh -o StrictHostKeyChecking no -q 1 ls -lh"
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	c.Assert(commandmocker.Output(tmpdir), Equals, output)
	c.Assert(buf.String(), Equals, output+"\n")
}

func (s *S) TestExecuteCommandUnitDown(c *C) {
	var buf bytes.Buffer
	tmpdir, err := commandmocker.Add("juju", "$*")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("almah", "static", 3)
	app.SetUnitStatus(provision.StatusDown, 1)
	p := JujuProvisioner{}
	err = p.ExecuteCommand(&buf, &buf, app, "ls", "-lha")
	c.Assert(err, IsNil)
	cmdOutput := "ssh -o StrictHostKeyChecking no -q 1 ls -lha"
	cmdOutput += "ssh -o StrictHostKeyChecking no -q 3 ls -lha"
	bufOutput := `Output from unit "almah/0":

ssh -o StrictHostKeyChecking no -q 1 ls -lha

Output from unit "almah/1":

Unit state is "down", it must be "started" for running commands.

Output from unit "almah/2":

ssh -o StrictHostKeyChecking no -q 3 ls -lha
`
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	c.Assert(commandmocker.Output(tmpdir), Equals, cmdOutput)
	c.Assert(buf.String(), Equals, bufOutput)
}

func (s *S) TestCollectStatus(c *C) {
	tmpdir, err := commandmocker.Add("juju", collectOutput)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	p := JujuProvisioner{}
	err = p.unitsCollection().Insert(instance{UnitName: "as_i_rise/0", InstanceId: "i-00000439"})
	c.Assert(err, IsNil)
	defer p.unitsCollection().Remove(bson.M{"_id": bson.M{"$in": []string{"as_i_rise/0", "the_infanta/0"}}})
	expected := []provision.Unit{
		{
			Name:       "as_i_rise/0",
			AppName:    "as_i_rise",
			Type:       "django",
			Machine:    105,
			InstanceId: "i-00000439",
			Ip:         "10.10.10.163",
			Status:     provision.StatusStarted,
		},
		{
			Name:       "the_infanta/0",
			AppName:    "the_infanta",
			Type:       "gunicorn",
			Machine:    107,
			InstanceId: "i-0000043e",
			Ip:         "10.10.10.168",
			Status:     provision.StatusInstalling,
		},
	}
	units, err := p.CollectStatus()
	c.Assert(err, IsNil)
	cp := make([]provision.Unit, len(units))
	copy(cp, units)
	if cp[0].Type == "gunicorn" {
		cp[0], cp[1] = cp[1], cp[0]
	}
	c.Assert(cp, DeepEquals, expected)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	done := make(chan int8)
	go func() {
		for {
			ct, err := p.unitsCollection().Find(nil).Count()
			c.Assert(err, IsNil)
			if ct == 2 {
				done <- 1
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2e9):
		c.Fatal("Did not save the unit after 2 seconds.")
	}
	var instances []instance
	err = p.unitsCollection().Find(nil).Sort("_id").All(&instances)
	c.Assert(err, IsNil)
	c.Assert(instances, HasLen, 2)
	c.Assert(instances[0].UnitName, Equals, "as_i_rise/0")
	c.Assert(instances[0].InstanceId, Equals, "i-00000439")
	c.Assert(instances[1].UnitName, Equals, "the_infanta/0")
	c.Assert(instances[1].InstanceId, Equals, "i-0000043e")
}

func (s *S) TestCollectStatusDirtyOutput(c *C) {
	tmpdir, err := commandmocker.Add("juju", dirtyCollectOutput)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	expected := []provision.Unit{
		{
			Name:       "as_i_rise/0",
			AppName:    "as_i_rise",
			Type:       "django",
			Machine:    105,
			InstanceId: "i-00000439",
			Ip:         "10.10.10.163",
			Status:     provision.StatusStarted,
		},
		{
			Name:       "the_infanta/1",
			AppName:    "the_infanta",
			Type:       "gunicorn",
			Machine:    107,
			InstanceId: "i-0000043e",
			Ip:         "10.10.10.168",
			Status:     provision.StatusInstalling,
		},
	}
	p := JujuProvisioner{}
	units, err := p.CollectStatus()
	c.Assert(err, IsNil)
	cp := make([]provision.Unit, len(units))
	copy(cp, units)
	if cp[0].Type == "gunicorn" {
		cp[0], cp[1] = cp[1], cp[0]
	}
	c.Assert(cp, DeepEquals, expected)
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		q := bson.M{"_id": bson.M{"$in": []string{"as_i_rise/0", "the_infanta/1"}}}
		for {
			if n, _ := p.unitsCollection().Find(q).Count(); n == 2 {
				break
			}
		}
		p.unitsCollection().Remove(q)
		wg.Done()
	}()
	wg.Wait()
}

func (s *S) TestCollectStatusIDChangeDisabledELB(c *C) {
	tmpdir, err := commandmocker.Add("juju", collectOutput)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	p := JujuProvisioner{}
	err = p.unitsCollection().Insert(instance{UnitName: "as_i_rise/0", InstanceId: "i-00000239"})
	c.Assert(err, IsNil)
	defer p.unitsCollection().Remove(bson.M{"_id": bson.M{"$in": []string{"as_i_rise/0", "the_infanta/0"}}})
	_, err = p.CollectStatus()
	c.Assert(err, IsNil)
	done := make(chan int8)
	go func() {
		for {
			q := bson.M{"_id": "as_i_rise/0", "instanceid": "i-00000439"}
			ct, err := p.unitsCollection().Find(q).Count()
			c.Assert(err, IsNil)
			if ct == 1 {
				done <- 1
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5e9):
		c.Fatal("Did not update the unit after 5 seconds.")
	}
	msg, err := getQueue(app.QueueName).Get(1e6)
	c.Assert(err, IsNil)
	defer msg.Delete()
	c.Assert(msg.Action, Equals, app.RegenerateApprcAndStart)
	c.Assert(msg.Args, DeepEquals, []string{"as_i_rise", "as_i_rise/0"})
}

func (s *S) TestCollectStatusIDChangeFromPending(c *C) {
	tmpdir, err := commandmocker.Add("juju", collectOutput)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	p := JujuProvisioner{}
	err = p.unitsCollection().Insert(instance{UnitName: "as_i_rise/0", InstanceId: "pending"})
	c.Assert(err, IsNil)
	defer p.unitsCollection().Remove(bson.M{"_id": bson.M{"$in": []string{"as_i_rise/0", "the_infanta/0"}}})
	_, err = p.CollectStatus()
	c.Assert(err, IsNil)
	done := make(chan int8)
	go func() {
		for {
			q := bson.M{"_id": "as_i_rise/0", "instanceid": "i-00000439"}
			ct, err := p.unitsCollection().Find(q).Count()
			c.Assert(err, IsNil)
			if ct == 1 {
				done <- 1
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5e9):
		c.Fatal("Did not update the unit after 5 seconds.")
	}
	_, err = getQueue(app.QueueName).Get(1e6)
	c.Assert(err, NotNil)
}

func (s *S) TestCollectStatusFailure(c *C) {
	tmpdir, err := commandmocker.Error("juju", "juju failed", 1)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	p := JujuProvisioner{}
	_, err = p.CollectStatus()
	c.Assert(err, NotNil)
	pErr, ok := err.(*provision.Error)
	c.Assert(ok, Equals, true)
	c.Assert(pErr.Reason, Equals, "juju failed")
	c.Assert(pErr.Err.Error(), Equals, "exit status 1")
	c.Assert(commandmocker.Ran(tmpdir), Equals, true)
}

func (s *S) TestCollectStatusInvalidYAML(c *C) {
	tmpdir, err := commandmocker.Add("juju", "local: somewhere::")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	p := JujuProvisioner{}
	_, err = p.CollectStatus()
	c.Assert(err, NotNil)
	pErr, ok := err.(*provision.Error)
	c.Assert(ok, Equals, true)
	c.Assert(pErr.Reason, Equals, `"juju status" returned invalid data: local: somewhere::`)
	c.Assert(pErr.Err, ErrorMatches, `^YAML error:.*$`)
}

func (s *S) TestLoadBalancerEnabledElb(c *C) {
	p := JujuProvisioner{}
	p.elb = new(bool)
	*p.elb = true
	lb := p.LoadBalancer()
	c.Assert(lb, NotNil)
}

func (s *S) TestLoadBalancerDisabledElb(c *C) {
	p := JujuProvisioner{}
	p.elb = new(bool)
	lb := p.LoadBalancer()
	c.Assert(lb, IsNil)
}

func (s *S) TestExecWithTimeout(c *C) {
	var data = []struct {
		cmd     []string
		timeout time.Duration
		out     string
		err     error
	}{
		{
			cmd:     []string{"sleep", "2"},
			timeout: 1e6,
			out:     "",
			err:     errors.New(`"sleep 2" ran for more than 1ms.`),
		},
		{
			cmd:     []string{"python", "-c", "import time; time.sleep(1); print 'hello world!'"},
			timeout: 5e9,
			out:     "hello world!\n",
			err:     nil,
		},
		{
			cmd:     []string{"python", "-c", "import sys; print 'hello world!'; exit(1)"},
			timeout: 5e9,
			out:     "hello world!\n",
			err:     errors.New("exit status 1"),
		},
	}
	for _, d := range data {
		out, err := execWithTimeout(d.timeout, d.cmd[0], d.cmd[1:]...)
		if string(out) != d.out {
			c.Errorf("Output. Want %q. Got %q.", d.out, out)
		}
		if d.err == nil && err != nil {
			c.Errorf("Error. Want %v. Got %v.", d.err, err)
		} else if d.err != nil && err.Error() != d.err.Error() {
			c.Errorf("Error message. Want %q. Got %q.", d.err.Error(), err.Error())
		}
	}
}

func (s *S) TestUnitStatus(c *C) {
	var tests = []struct {
		instance     string
		agent        string
		machineAgent string
		expected     provision.Status
	}{
		{"something", "nothing", "wut", provision.StatusPending},
		{"", "", "", provision.StatusCreating},
		{"", "", "pending", provision.StatusCreating},
		{"", "", "not-started", provision.StatusCreating},
		{"pending", "", "", provision.StatusCreating},
		{"", "not-started", "running", provision.StatusCreating},
		{"error", "install-error", "start-error", provision.StatusError},
		{"started", "start-error", "running", provision.StatusError},
		{"running", "pending", "running", provision.StatusInstalling},
		{"running", "started", "running", provision.StatusStarted},
		{"running", "down", "running", provision.StatusDown},
	}
	for _, t := range tests {
		got := unitStatus(t.instance, t.agent, t.machineAgent)
		if got != t.expected {
			c.Errorf("unitStatus(%q, %q, %q): Want %q. Got %q.", t.instance, t.agent, t.machineAgent, t.expected, got)
		}
	}
}

func (s *S) TestAddr(c *C) {
	app := testing.NewFakeApp("blue", "who", 1)
	p := JujuProvisioner{}
	addr, err := p.Addr(app)
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, app.ProvisionUnits()[0].GetIp())
}

func (s *S) TestAddrWithoutUnits(c *C) {
	app := testing.NewFakeApp("squeeze", "who", 0)
	p := JujuProvisioner{}
	addr, err := p.Addr(app)
	c.Assert(addr, Equals, "")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, `App "squeeze" has no units.`)
}

func (s *ELBSuite) TestProvisionWithELB(c *C) {
	config.Set("juju:charms-path", "/home/charms")
	defer config.Unset("juju:charms-path")
	tmpdir, err := commandmocker.Add("juju", "deployed")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("jimmy", "who", 0)
	p := JujuProvisioner{}
	err = p.Provision(app)
	c.Assert(err, IsNil)
	lb := p.LoadBalancer()
	defer lb.Destroy(app)
	addr, err := lb.Addr(app)
	c.Assert(err, IsNil)
	c.Assert(addr, Not(Equals), "")
	msg, err := getQueue(queueName).Get(1e9)
	c.Assert(err, IsNil)
	defer msg.Delete()
	c.Assert(msg.Action, Equals, addUnitToLoadBalancer)
	c.Assert(msg.Args, DeepEquals, []string{"jimmy"})
}

func (s *ELBSuite) TestDestroyWithELB(c *C) {
	config.Set("juju:charms-path", "/home/charms")
	defer config.Unset("juju:charms-path")
	tmpdir, err := commandmocker.Add("juju", "deployed")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("jimmy", "who", 0)
	p := JujuProvisioner{}
	err = p.Provision(app)
	c.Assert(err, IsNil)
	err = p.Destroy(app)
	c.Assert(err, IsNil)
	lb := p.LoadBalancer()
	defer lb.Destroy(app) // sanity
	addr, err := lb.Addr(app)
	c.Assert(addr, Equals, "")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "not found")
	q := getQueue(queueName)
	msg, err := q.Get(1e9)
	c.Assert(err, IsNil)
	if msg.Action == addUnitToLoadBalancer && msg.Args[0] == "jimmy" {
		msg.Delete()
	} else {
		q.Release(msg, 0)
	}
}

func (s *ELBSuite) TestAddUnitsWithELB(c *C) {
	tmpdir, err := commandmocker.Add("juju", addUnitsOutput)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("resist", "rush", 0)
	p := JujuProvisioner{}
	_, err = p.AddUnits(app, 4)
	c.Assert(err, IsNil)
	expected := []string{
		"resist", "resist/3", "resist/4",
		"resist/5", "resist/6",
	}
	msg, err := getQueue(queueName).Get(1e9)
	c.Assert(err, IsNil)
	defer msg.Delete()
	c.Assert(msg.Action, Equals, addUnitToLoadBalancer)
	c.Assert(msg.Args, DeepEquals, expected)
}

func (s *ELBSuite) TestRemoveUnitWithELB(c *C) {
	instIds := make([]string, 4)
	units := make([]provision.Unit, len(instIds))
	for i := 0; i < len(instIds); i++ {
		id := s.server.NewInstance()
		defer s.server.RemoveInstance(id)
		instIds[i] = id
		units[i] = provision.Unit{
			Name:       "radio/" + strconv.Itoa(i),
			InstanceId: id,
		}
	}
	tmpdir, err := commandmocker.Add("juju", "unit removed")
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	app := testing.NewFakeApp("radio", "rush", 4)
	manager := ELBManager{}
	manager.e = s.client
	err = manager.Create(app)
	c.Assert(err, IsNil)
	defer manager.Destroy(app)
	err = manager.Register(app, units...)
	c.Assert(err, IsNil)
	p := JujuProvisioner{}
	fUnit := testing.FakeUnit{Name: units[0].Name, InstanceId: units[0].InstanceId}
	err = p.removeUnit(app, &fUnit)
	c.Assert(err, IsNil)
	resp, err := s.client.DescribeLoadBalancers(app.GetName())
	c.Assert(err, IsNil)
	c.Assert(resp.LoadBalancerDescriptions, HasLen, 1)
	c.Assert(resp.LoadBalancerDescriptions[0].Instances, HasLen, len(units)-1)
	instance := resp.LoadBalancerDescriptions[0].Instances[0]
	c.Assert(instance.InstanceId, Equals, instIds[1])
}

func (s *ELBSuite) TestCollectStatusWithELBAndIDChange(c *C) {
	a := testing.NewFakeApp("symfonia", "symfonia", 0)
	p := JujuProvisioner{}
	lb := p.LoadBalancer()
	err := lb.Create(a)
	c.Assert(err, IsNil)
	defer lb.Destroy(a)
	id1 := s.server.NewInstance()
	defer s.server.RemoveInstance(id1)
	id2 := s.server.NewInstance()
	defer s.server.RemoveInstance(id2)
	id3 := s.server.NewInstance()
	defer s.server.RemoveInstance(id3)
	err = p.unitsCollection().Insert(instance{UnitName: "symfonia/0", InstanceId: id3})
	c.Assert(err, IsNil)
	err = lb.Register(a, provision.Unit{InstanceId: id3}, provision.Unit{InstanceId: id2})
	q := bson.M{"_id": bson.M{"$in": []string{"symfonia/0", "symfonia/1", "symfonia/2", "raise/0"}}}
	defer p.unitsCollection().Remove(q)
	output := strings.Replace(simpleCollectOutput, "i-00004444", id1, 1)
	output = strings.Replace(output, "i-00004445", id2, 1)
	tmpdir, err := commandmocker.Add("juju", output)
	c.Assert(err, IsNil)
	defer commandmocker.Remove(tmpdir)
	_, err = p.CollectStatus()
	c.Assert(err, IsNil)
	done := make(chan int8)
	go func() {
		for {
			q := bson.M{"_id": "symfonia/0", "instanceid": id1}
			ct, err := p.unitsCollection().Find(q).Count()
			c.Assert(err, IsNil)
			if ct == 1 {
				done <- 1
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5e9):
		c.Fatal("Did not save the unit after 5 seconds.")
	}
	resp, err := s.client.DescribeLoadBalancers(a.GetName())
	c.Assert(err, IsNil)
	c.Assert(resp.LoadBalancerDescriptions, HasLen, 1)
	instances := resp.LoadBalancerDescriptions[0].Instances
	c.Assert(instances, HasLen, 2)
	c.Assert(instances[0].InstanceId, Equals, id2)
	c.Assert(instances[1].InstanceId, Equals, id1)
	msg, err := getQueue(app.QueueName).Get(1e9)
	c.Assert(err, IsNil)
	c.Assert(msg.Args, DeepEquals, []string{"symfonia", "symfonia/0"})
	msg.Delete()
}

func (s *ELBSuite) TestAddrWithELB(c *C) {
	app := testing.NewFakeApp("jimmy", "who", 0)
	p := JujuProvisioner{}
	lb := p.LoadBalancer()
	err := lb.Create(app)
	c.Assert(err, IsNil)
	defer lb.Destroy(app)
	addr, err := p.Addr(app)
	c.Assert(err, IsNil)
	lAddr, err := lb.Addr(app)
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, lAddr)
}

func (s *ELBSuite) TestAddrWithUnknownELB(c *C) {
	app := testing.NewFakeApp("jimmy", "who", 0)
	p := JujuProvisioner{}
	addr, err := p.Addr(app)
	c.Assert(addr, Equals, "")
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "not found")
}
