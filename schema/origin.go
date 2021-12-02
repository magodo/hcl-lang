package schema

import (
	"sort"

	"github.com/hashicorp/hcl-lang/lang"
	"github.com/zclconf/go-cty/cty"
)

type ReferenceProxy struct {
	LocalAddr  lang.Address
	TargetAddr lang.Address
	TargetPath lang.Path
}

type ReferenceProxies []ReferenceProxy

func ReferenceProxiesForValue(localAddr lang.Address, targetAddr lang.ScopeId, val cty.Value) ReferenceProxies {
	if val.IsNull() {
		return nil
	}
	typ := val.Type()

	if typ.IsPrimitiveType() || typ == cty.DynamicPseudoType {
		return nil
	}

	if typ.IsSetType() {
		// set elements are not addressable
		return nil
	}

	proxies := make(ReferenceProxies, 0)

	if typ.IsObjectType() {
		for key := range typ.AttributeTypes() {
			elAddr := address.Copy()
			elAddr = append(elAddr, lang.AttrStep{Name: key})

			proxies = append(proxies,
				targetableForValue(elAddr, scopeId, val.GetAttr(key)))
		}
	}

	if typ.IsMapType() {
		for key, val := range val.AsValueMap() {
			elAddr := address.Copy()
			elAddr = append(elAddr, lang.IndexStep{Key: cty.StringVal(key)})

			proxies = append(proxies,
				targetableForValue(elAddr, scopeId, val))
		}
	}

	if typ.IsListType() || typ.IsTupleType() {
		for i, val := range val.AsValueSlice() {
			elAddr := address.Copy()
			elAddr = append(elAddr, lang.IndexStep{Key: cty.NumberIntVal(int64(i))})

			proxies = append(proxies,
				targetableForValue(elAddr, scopeId, val))
		}
	}

	sort.Sort(proxies)

	return proxies
}
