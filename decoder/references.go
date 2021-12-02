package decoder

import (
	"sort"

	"github.com/hashicorp/hcl-lang/lang"
	"github.com/hashicorp/hcl-lang/reference"
	"github.com/hashicorp/hcl-lang/schema"
	"github.com/hashicorp/hcl/v2"
)

func (d *PathDecoder) CollectReferences() (reference.References, error) {
	if d.pathCtx.Schema == nil {
		// unable to collect reference origins without schema
		return reference.References{}, &NoSchemaError{}
	}

	uRefs := unresolvedReferences{
		Origins: make(reference.Origins, 0),
		Targets: make(reference.Targets, 0),
		Proxies: make(schema.ReferenceProxies, 0),
	}

	files := d.filenames()
	for _, filename := range files {
		f, err := d.fileByName(filename)
		if err != nil {
			// skip unparseable file
			continue
		}

		bodyRefs := d.collectReferencesInBody(f.Body, d.pathCtx.Schema)
		uRefs.Origins = append(uRefs.Origins, bodyRefs.Origins...)
		uRefs.Proxies = append(uRefs.Proxies, bodyRefs.Proxies...)
		uRefs.Targets = append(uRefs.Targets, bodyRefs.Targets...)
	}

	refs := resolveReferenceProxies(uRefs)

	sort.SliceStable(refs.Origins, func(i, j int) bool {
		return refs.Origins[i].OriginRange().Filename <= refs.Origins[i].OriginRange().Filename &&
			refs.Origins[i].OriginRange().Start.Byte < refs.Origins[j].OriginRange().Start.Byte
	})

	return refs, nil
}

type unresolvedReferences struct {
	Origins reference.Origins
	Targets reference.Targets
	Proxies schema.ReferenceProxies
}

func (d *PathDecoder) collectReferencesInBody(body hcl.Body, bodySchema *schema.BodySchema) unresolvedReferences {
	if bodySchema == nil {
		return unresolvedReferences{}
	}

	refs := unresolvedReferences{
		Origins: make(reference.Origins, 0),
		Targets: make(reference.Targets, 0),
		Proxies: make(schema.ReferenceProxies, 0),
	}

	content := decodeBody(body, bodySchema)

	for _, attr := range content.Attributes {
		aSchema, ok := bodySchema.Attributes[attr.Name]
		if !ok {
			if bodySchema.AnyAttribute == nil {
				// skip unknown attribute
				continue
			}
			aSchema = bodySchema.AnyAttribute
		}

		refs.Origins = append(refs.Origins, d.collectOriginsInAttribute(attr, aSchema)...)
		refs.Targets = append(refs.Targets, decodeReferenceTargetsForAttribute(attr, aSchema)...)
	}

	for _, block := range content.Blocks {
		bSchema, ok := bodySchema.Blocks[block.Type]
		if !ok {
			// unknown block (no schema)
			continue
		}

		bTargets := d.collectTargetsInBlockHeader(block, bSchema)
		refs.Targets = append(refs.Targets, bTargets...)

		mergedSchema, err := mergeBlockBodySchemas(block.Block, bSchema)
		if err != nil {
			continue
		}

		for _, tb := range mergedSchema.TargetableAs {
			refs.Targets = append(refs.Targets, decodeTargetableBlockContent(block, tb))
		}

		for _, op := range mergedSchema.ReferenceProxies {
			refs.Proxies = append(refs.Proxies, op)
		}

		bodyRefs := d.collectReferencesInBody(block.Body, mergedSchema)
		refs.Origins = append(refs.Origins, bodyRefs.Origins...)
		refs.Targets = append(refs.Targets, bodyRefs.Targets...)
	}

	return refs
}

func resolveReferenceProxies(refs unresolvedReferences) reference.References {
	extraOrigins := make(reference.Origins, 0)

	for _, proxy := range refs.Proxies {
		localOrigins := matchingLocalOrigins(refs.Origins, proxy.LocalAddr)
		for _, localOrigin := range localOrigins {
			extraOrigins = append(extraOrigins, reference.PathOrigin{
				Range:       localOrigin.OriginRange(),
				TargetAddr:  proxy.TargetAddr,
				TargetPath:  proxy.TargetPath,
				Constraints: localOrigin.OriginConstraints(),
			})
		}
	}

	return reference.References{
		Origins: append(refs.Origins, extraOrigins...),
		Targets: refs.Targets,
	}
}

func matchingLocalOrigins(origins reference.Origins, addr lang.Address) reference.Origins {
	matchingOrigins := make(reference.Origins, 0)

	for _, origin := range origins {
		_, ok := origin.(*reference.LocalOrigin)
		if !ok {
			continue
		}

		if origin.Address().Equals(addr) {
			matchingOrigins = append(matchingOrigins, origin)
		}
	}

	return matchingOrigins
}

func (d *PathDecoder) collectOriginsInAttribute(attr *hcl.Attribute, aSchema *schema.AttributeSchema) reference.Origins {
	origins := make(reference.Origins, 0)

	if aSchema.OriginForTarget != nil {
		targetAddr, ok := resolveAttributeAddress(attr, aSchema.OriginForTarget.Address)
		if ok {
			origins = append(origins, reference.PathOrigin{
				Range:      attr.NameRange,
				TargetAddr: targetAddr,
				TargetPath: aSchema.OriginForTarget.Path,
				Constraints: reference.OriginConstraints{
					{
						OfScopeId: aSchema.OriginForTarget.Constraints.ScopeId,
						OfType:    aSchema.OriginForTarget.Constraints.Type,
					},
				},
			})
		}
	}

	origins = append(origins, d.findOriginsInExpression(attr.Expr, aSchema.Expr)...)

	return origins
}

func (d *PathDecoder) collectTargetsInBlockHeader(blk *blockContent, bSchema *schema.BlockSchema) reference.Targets {
	targets := make(reference.Targets, 0)

	addr, ok := resolveBlockAddress(blk.Block, bSchema)
	if !ok {
		// skip unresolvable address
		return targets
	}

	if bSchema.Address.AsReference {
		ref := reference.Target{
			Addr:        addr,
			ScopeId:     bSchema.Address.ScopeId,
			DefRangePtr: blk.DefRange.Ptr(),
			RangePtr:    blk.Range.Ptr(),
			Name:        bSchema.Address.FriendlyName,
		}
		targets = append(targets, ref)
	}

	if bSchema.Address.AsTypeOf != nil {
		targets = append(targets, referenceAsTypeOf(blk.Block, blk.Range.Ptr(), bSchema, addr)...)
	}

	var bodyRef reference.Target

	if bSchema.Address.BodyAsData {
		bodyRef = reference.Target{
			Addr:        addr,
			ScopeId:     bSchema.Address.ScopeId,
			DefRangePtr: blk.DefRange.Ptr(),
			RangePtr:    blk.Range.Ptr(),
		}

		if bSchema.Body != nil {
			bodyRef.Description = bSchema.Body.Description
		}

		if bSchema.Address.InferBody && bSchema.Body != nil {
			bodyRef.NestedTargets = append(bodyRef.NestedTargets,
				d.collectInferredReferenceTargetsForBody(addr, bSchema.Address.ScopeId, blk.Body, bSchema.Body)...)
		}

		bodyRef.Type = bodyToDataType(bSchema.Type, bSchema.Body)

		targets = append(targets, bodyRef)
	}

	if bSchema.Address.DependentBodyAsData {
		if !bSchema.Address.BodyAsData {
			bodyRef = reference.Target{
				Addr:        addr,
				ScopeId:     bSchema.Address.ScopeId,
				DefRangePtr: blk.DefRange.Ptr(),
				RangePtr:    blk.Range.Ptr(),
			}
		}

		depSchema, _, ok := NewBlockSchema(bSchema).DependentBodySchema(blk.Block)
		if ok {
			fullSchema := depSchema
			if bSchema.Address.BodyAsData {
				mergedSchema, err := mergeBlockBodySchemas(blk.Block, bSchema)
				if err != nil {
					return targets
				}
				bodyRef.NestedTargets = make(reference.Targets, 0)
				fullSchema = mergedSchema
			}

			bodyRef.Type = bodyToDataType(bSchema.Type, fullSchema)

			if bSchema.Address.InferDependentBody && len(bSchema.DependentBody) > 0 {
				bodyRef.NestedTargets = append(bodyRef.NestedTargets,
					d.collectInferredReferenceTargetsForBody(addr, bSchema.Address.ScopeId, blk.Body, fullSchema)...)
			}

			if !bSchema.Address.BodyAsData {
				targets = append(targets, bodyRef)
			}
		}
	}

	sort.Sort(bodyRef.NestedTargets)

	return targets
}

func decodeTargetableBlockContent(bContent *blockContent, tt *schema.Targetable) reference.Target {
	target := reference.Target{
		Addr:        tt.Address.Copy(),
		ScopeId:     tt.ScopeId,
		RangePtr:    bContent.Range.Ptr(),
		DefRangePtr: bContent.DefRange.Ptr(),
		Type:        tt.AsType,
		Description: tt.Description,
	}

	if tt.NestedTargetables != nil {
		target.NestedTargets = make(reference.Targets, len(tt.NestedTargetables))
		for i, ntt := range tt.NestedTargetables {
			target.NestedTargets[i] = decodeTargetableBlockContent(bContent, ntt)
		}
	}

	return target
}
