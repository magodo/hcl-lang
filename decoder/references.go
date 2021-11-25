package decoder

import (
	"sort"

	"github.com/hashicorp/hcl-lang/reference"
)

func (d *PathDecoder) CollectReferences() (reference.References, error) {
	if d.pathCtx.Schema == nil {
		// unable to collect reference origins without schema
		return reference.References{}, &NoSchemaError{}
	}

	refs := reference.References{
		Origins: make(reference.Origins, 0),
		Targets: make(reference.Targets, 0),
	}

	files := d.filenames()
	for _, filename := range files {
		f, err := d.fileByName(filename)
		if err != nil {
			// skip unparseable file
			continue
		}

		refs.Targets = append(refs.Targets, d.decodeReferenceTargetsForBody(f.Body, nil, d.pathCtx.Schema)...)
		refs.Origins = append(refs.Origins, d.referenceOriginsInBody(f.Body, d.pathCtx.Schema)...)
	}

	sort.SliceStable(refs.Origins, func(i, j int) bool {
		return refs.Origins[i].OriginRange().Filename <= refs.Origins[i].OriginRange().Filename &&
			refs.Origins[i].OriginRange().Start.Byte < refs.Origins[j].OriginRange().Start.Byte
	})

	return refs, nil
}
