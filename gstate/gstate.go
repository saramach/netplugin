/***
Copyright 2014 Cisco Systems Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package gstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"

	"github.com/jainvipin/bitset"

	"github.com/contiv/netplugin/core"
	"github.com/contiv/netplugin/netutils"
)

const (
	BASE_GLOBAL        = "/contiv/"
	CFG_GLOBAL_PREFIX  = BASE_GLOBAL + "config/global/"
	CFG_GLOBAL_PATH    = CFG_GLOBAL_PREFIX + "%s"
	OPER_GLOBAL_PREFIX = BASE_GLOBAL + "oper/global/"
	OPER_GLOBAL_PATH   = OPER_GLOBAL_PREFIX + "%s"
)

const (
	VersionBeta1 = "0.01"
)

// specifies various parameters to choose the auto allocation values to pick from
// this allows mostly hands-free allocation of networks, endpoints, attach/detach
// operations without having to specify these each time an entity gets created
type AutoParams struct {
	SubnetPool     string
	SubnetLen      uint
	AllocSubnetLen uint
	Vlans          string
	Vxlans         string
}

// specifies parameters that decides the deployment choices
type DeployParams struct {
	DefaultNetType string
}

// global state of the network plugin
type Cfg struct {
	StateDriver core.StateDriver
	Version     string
	Tenant      string
	Auto        AutoParams
	Deploy      DeployParams
}

type Oper struct {
	StateDriver     core.StateDriver
	Tenant          string
	DefaultNetType  string
	SubnetPool      string
	SubnetLen       uint
	AllocSubnetLen  uint
	FreeSubnets     bitset.BitSet
	FreeVlans       bitset.BitSet
	FreeLocalVlans  bitset.BitSet
	FreeVxlansStart uint
	FreeVxlans      bitset.BitSet
}

func (gc *Cfg) Dump() error {
	log.Printf("Global State %v \n", gc)
	return nil
}

func (gc *Cfg) checkErrors() error {
	var err error

	if net.ParseIP(gc.Auto.SubnetPool) == nil {
		return errors.New(fmt.Sprintf("invalid ip address pool %s",
			gc.Auto.SubnetPool))
	}

	_, err = netutils.ParseTagRanges(gc.Auto.Vlans, "vlan")
	if err != nil {
		return err
	}

	_, err = netutils.ParseTagRanges(gc.Auto.Vxlans, "vxlan")
	if err != nil {
		return err
	}

	if gc.Deploy.DefaultNetType != "vlan" &&
		gc.Deploy.DefaultNetType != "vxlan" {
		return errors.New(fmt.Sprintf("unsupported net type %s",
			gc.Deploy.DefaultNetType))
	}

	if gc.Auto.SubnetLen > gc.Auto.AllocSubnetLen {
		return errors.New(fmt.Sprintf(
			"subnet size %d is smaller than subnets to be allocated from it",
			gc.Auto.SubnetLen, gc.Auto.AllocSubnetLen))
	}
	return err
}

func Parse(configBytes []byte) (*Cfg, error) {
	var gc Cfg

	err := json.Unmarshal(configBytes, &gc)
	if err != nil {
		return nil, err
	}

	err = gc.checkErrors()
	if err != nil {
		return nil, err
	}

	return &gc, err
}

func (gc *Cfg) Write() error {
	key := fmt.Sprintf(CFG_GLOBAL_PATH, gc.Tenant)
	return gc.StateDriver.WriteState(key, gc, json.Marshal)
}

func (gc *Cfg) Read(tenant string) error {
	key := fmt.Sprintf(CFG_GLOBAL_PATH, tenant)
	return gc.StateDriver.ReadState(key, gc, json.Unmarshal)
}

func ReadAllGlobalCfg(d core.StateDriver) ([]*Cfg, error) {
	values := []*Cfg{}
	byteValues, err := d.ReadAll(CFG_GLOBAL_PREFIX)
	if err != nil {
		return nil, err
	}
	for _, byteValue := range byteValues {
		value := &Cfg{StateDriver: d}
		err = json.Unmarshal(byteValue, value)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (gc *Cfg) Clear() error {
	key := fmt.Sprintf(CFG_GLOBAL_PATH, gc.Tenant)
	return gc.StateDriver.ClearState(key)
}

func (g *Oper) Write() error {
	key := fmt.Sprintf(OPER_GLOBAL_PATH, g.Tenant)
	return g.StateDriver.WriteState(key, g, json.Marshal)
}

func (g *Oper) Read(tenant string) error {
	key := fmt.Sprintf(OPER_GLOBAL_PATH, tenant)
	return g.StateDriver.ReadState(key, g, json.Unmarshal)
}

func (g *Oper) Clear() error {
	key := fmt.Sprintf(OPER_GLOBAL_PATH, g.Tenant)
	return g.StateDriver.ClearState(key)
}

func (g *Oper) initVxlanBitset(vxlans string, vlans string,
	defPktType string) error {
	var vxlanRange netutils.TagRange

	g.FreeVxlans = *netutils.CreateBitset(14)

	if defPktType == "vxlan" && vlans == "" {
		g.FreeLocalVlans = g.FreeVlans
		g.FreeVlans = *g.FreeVlans.Complement()
		clearReservedVlans(&g.FreeVlans)
	} else {
		g.FreeLocalVlans = *g.FreeVlans.Complement()
		clearReservedVlans(&g.FreeLocalVlans)
	}

	if vxlans == "" {
		vxlanRange.Min = 10000
		vxlanRange.Max = 26000
	} else {
		vxlanRanges, err := netutils.ParseTagRanges(vxlans, "vxlan")
		if err != nil {
			return err
		}
		vxlanRange = vxlanRanges[0]
	}

	g.FreeVxlansStart = uint(vxlanRange.Min)
	for vxlan := vxlanRange.Min; vxlan <= vxlanRange.Max; vxlan++ {
		g.FreeVxlans.Set(uint(vxlan - vxlanRange.Min))
	}

	return nil
}

func (g *Oper) AllocVxlan() (vxlan uint, localVlan uint, err error) {
	var ok bool

	vxlan, ok = g.FreeVxlans.NextSet(0)
	if !ok {
		err = errors.New("no Vxlans available ")
		return
	}

	localVlan, ok = g.FreeLocalVlans.NextSet(0)
	if !ok {
		err = errors.New("no local vlans available ")
		return
	}

	g.FreeLocalVlans.Clear(localVlan)
	g.FreeVxlans.Clear(vxlan)
	vxlan = vxlan + g.FreeVxlansStart

	return
}

func (g *Oper) FreeVxlan(vxlan uint, localVlan uint) error {
	if !g.FreeLocalVlans.Test(localVlan) {
		g.FreeLocalVlans.Set(localVlan)
	}

	vxlan = vxlan - g.FreeVxlansStart
	if !g.FreeVxlans.Test(vxlan) {
		g.FreeVxlans.Set(vxlan)
	}

	return nil
}

func (g *Oper) AllocLocalVlan() (uint, error) {
	vlan, ok := g.FreeLocalVlans.NextSet(0)
	if !ok {
		return 0, errors.New("no vlans available ")
	}

	g.FreeLocalVlans.Clear(vlan)

	return vlan, nil
}

// be idempotent, don't complain if vlan is already freed
func (g *Oper) FreeLocalVlan(vlan uint) error {
	if !g.FreeLocalVlans.Test(vlan) {
		g.FreeLocalVlans.Set(vlan)
	}
	return nil
}

func clearReservedVlans(vlanBitset *bitset.BitSet) {
	vlanBitset.Clear(0)
	vlanBitset.Clear(4095)
}

func (g *Oper) initVlanBitset(vlans string) error {

	g.FreeVlans = *netutils.CreateBitset(12)

	if vlans == "" {
		vlans = "1-4094"
	}

	vlanRanges, err := netutils.ParseTagRanges(vlans, "vlan")
	if err != nil {
		return err
	}

	for _, vlanRange := range vlanRanges {
		for vlan := vlanRange.Min; vlan <= vlanRange.Max; vlan++ {
			g.FreeVlans.Set(uint(vlan))
		}
	}

	return nil
}

func (g *Oper) AllocVlan() (uint, error) {
	vlan, ok := g.FreeVlans.NextSet(0)
	if !ok {
		return 0, errors.New("no vlans available ")
	}

	g.FreeVlans.Clear(vlan)

	return vlan, nil
}

// be idempotent, don't complain if vlan is already freed
func (g *Oper) FreeVlan(vlan uint) error {
	if !g.FreeVlans.Test(vlan) {
		g.FreeVlans.Set(vlan)
	}
	return nil
}

func (g *Oper) CheckVlanInUse(vlan uint) error {
	if !g.FreeVlans.Test(vlan) {
		return errors.New("specified vlan not available")
	}

	return nil
}

func (g *Oper) SetVlan(vlan uint) (err error) {
	err = g.CheckVlanInUse(vlan)
	if err == nil {
		g.FreeVlans.Clear(vlan)
	}

	return
}

func (g *Oper) AllocSubnet() (string, error) {

	subnetId, found := g.FreeSubnets.NextSet(0)
	if !found {
		log.Printf("Bitmap: %s \n", g.FreeSubnets.DumpAsBits())
		return "", errors.New("subnet exhaustion")
	}

	g.FreeSubnets.Clear(subnetId)
	subnetIp, err := netutils.GetSubnetIp(g.SubnetPool, g.SubnetLen,
		g.AllocSubnetLen, subnetId)
	if err != nil {
		return "", err
	}

	return subnetIp, nil
}

func (g *Oper) FreeSubnet(subnetIp string) error {
	subnetId, err := netutils.GetIpNumber(g.SubnetPool, g.SubnetLen,
		g.AllocSubnetLen, subnetIp)
	if err != nil {
		log.Printf("error '%s' getting subnetid for subnet %s/%d \n",
			err, subnetIp, g.SubnetLen)
	}
	g.FreeSubnets.Set(subnetId)

	return err
}

func (gc *Cfg) Process() (*Oper, error) {
	var err error

	if gc.Version != VersionBeta1 {
		return nil, errors.New(fmt.Sprintf("unsupported verison %s",
			gc.Version))
	}

	err = gc.checkErrors()
	if err != nil {
		return nil, errors.New(fmt.Sprintf(
			"process failed on error checks %s \n", err))
	}

	tenant := gc.Tenant
	if tenant == "" {
		return nil, errors.New("null tenant")
	}

	g := &Oper{
		StateDriver:    gc.StateDriver,
		Tenant:         gc.Tenant,
		SubnetLen:      gc.Auto.SubnetLen,
		DefaultNetType: gc.Deploy.DefaultNetType,
		AllocSubnetLen: gc.Auto.AllocSubnetLen,
		SubnetPool:     gc.Auto.SubnetPool}

	allocSubnetSize := gc.Auto.AllocSubnetLen - gc.Auto.SubnetLen

	g.FreeSubnets = *netutils.CreateBitset(allocSubnetSize).Complement()

	err = g.initVlanBitset(gc.Auto.Vlans)
	if err != nil {
		log.Printf("Error '%s' initializing vlans \n", err)
		return nil, err
	}

	err = g.initVxlanBitset(gc.Auto.Vxlans, gc.Auto.Vlans,
		gc.Deploy.DefaultNetType)
	if err != nil {
		log.Printf("Error '%s' initializing vlans \n", err)
		return nil, err
	}

	// log.Printf("updating the global config to new state %v \n", gc)
	return g, nil
}
