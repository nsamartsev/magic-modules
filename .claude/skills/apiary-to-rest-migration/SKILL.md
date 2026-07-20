---
name: apiary-to-rest-migration
description: >
  Migrate Terraform GCP provider resources in magic-modules from the legacy Apiary-generated
  compute library (struct-based, `NewClient(config, ua).Resource.Method().Do()` pattern) to
  direct REST HTTP calls using `transport_tpg.SendRequest()` and `map[string]interface{}` bodies.

  Trigger this skill whenever you see: `NewClient(config, userAgent).*.Do()` calls, imports of
  `google.golang.org/api/compute/v1` or `compute/v0.beta`, struct types like `*compute.Network`
  or `*compute.NetworkPeering`, `expandXxx` helper functions returning Apiary structs, or
  `ForceSendFields` usage. Also trigger for `.go.tmpl` resource files and `_test.go.tmpl` test
  files in `mmv1/third_party/terraform/services/compute/` that mix Apiary and REST patterns.
  Invoke even if the user just says "migrate this resource to REST" or pastes compute resource code.
---

# Apiary → REST Migration (magic-modules / terraform-provider-google)

## Why this migration exists

The Apiary mono-package (`google.golang.org/api/compute/v1`) pins all resources to a single
API version, blocks per-resource feature gating, and couples unrelated resources to each other's
breaking changes. Migrating to `transport_tpg.SendRequest()` gives each resource its own HTTP
path and API version control.

All handwritten resources live in `mmv1/third_party/terraform/services/compute/`. Files ending in
`.go.tmpl` use Go template directives for GA/beta variant generation — preserve all `{{- if ... }}`
blocks and the doubled-brace escaping for URL variables (explained below).

---

## Migration checklist

Work through these in order; each section has before/after patterns.

- [ ] Remove Apiary import (`compute/v1` or `compute/v0.beta`)
- [ ] Keep or add `transport_tpg` and `tpgresource` imports
- [ ] Convert `expand*` helpers: return `map[string]interface{}` instead of Apiary struct
- [ ] Handle `ForceSendFields` — include those fields explicitly in the map
- [ ] Convert `flatten*` / finder helpers: accept `map[string]interface{}` instead of structs
- [ ] Replace `NewClient(...).Resource.Method(args, body).Do()` with `SendRequest()`
- [ ] Build URLs with `tpgresource.ReplaceVars()` or `transport_tpg.BaseUrl()` + `fmt.Sprintf()`
- [ ] Pass the returned `map[string]interface{}` directly to `ComputeOperationWaitTime()`
- [ ] Read response fields via map type assertions (`res["fieldName"].(string)`)
- [ ] In test files: replace client `.Get().Do()` with `SendRequest()` + `transport_tpg.BaseUrl()`

---

## Pattern 1 — Apiary struct → map body

The `expand*` helpers that build request bodies change from returning Apiary structs to
returning `map[string]interface{}`. Fields in the map are always serialized, so you no
longer need `ForceSendFields` — just always include the field.

```go
// BEFORE
func expandNetworkPeering(d *schema.ResourceData) *compute.NetworkPeering {
    return &compute.NetworkPeering{
        Name:                           d.Get("name").(string),
        Network:                        d.Get("peer_network").(string),
        ExportCustomRoutes:             d.Get("export_custom_routes").(bool),
        ImportCustomRoutes:             d.Get("import_custom_routes").(bool),
        ExportSubnetRoutesWithPublicIp: d.Get("export_subnet_routes_with_public_ip").(bool),
        ImportSubnetRoutesWithPublicIp: d.Get("import_subnet_routes_with_public_ip").(bool),
        StackType:                      d.Get("stack_type").(string),
        ForceSendFields: []string{"ExportSubnetRoutesWithPublicIp", "ImportCustomRoutes"},
    }
}

// AFTER — drop ForceSendFields; map keys are camelCase JSON field names
func expandNetworkPeering(d *schema.ResourceData) map[string]interface{} {
    return map[string]interface{}{
        "name":                           d.Get("name").(string),
        "network":                        d.Get("peer_network").(string),
        "exportCustomRoutes":             d.Get("export_custom_routes").(bool),
        "importCustomRoutes":             d.Get("import_custom_routes").(bool),
        "exportSubnetRoutesWithPublicIp": d.Get("export_subnet_routes_with_public_ip").(bool),
        "importSubnetRoutesWithPublicIp": d.Get("import_subnet_routes_with_public_ip").(bool),
        "stackType":                      d.Get("stack_type").(string),
        "exchangeSubnetRoutes":           true,
    }
}
```

Map keys are **camelCase JSON field names** (matching the GCP REST API JSON schema), not Go struct field names.

---

## Pattern 2 — Struct-consuming helpers → map-consuming helpers

Helper functions that previously received Apiary struct pointers now receive `map[string]interface{}`:

```go
// BEFORE
func findPeeringFromNetwork(network *compute.Network, peeringName string) *compute.NetworkPeering {
    for _, p := range network.Peerings {
        if p.Name == peeringName {
            return p
        }
    }
    return nil
}

// AFTER
func findPeeringFromNetwork(network map[string]interface{}, peeringName string) map[string]interface{} {
    peerings, _ := network["peerings"].([]interface{})
    for _, p := range peerings {
        peering, _ := p.(map[string]interface{})
        if peering["name"].(string) == peeringName {
            return peering
        }
    }
    return nil
}
```

---

## Pattern 3 — API calls: NewClient → SendRequest

Replace `NewClient(config, userAgent).Resource.Method(...).Do()` with `transport_tpg.SendRequest()`.

### POST with body (insert, addPeering, removePeering, addInstance, etc.)

```go
// BEFORE
request := &compute.NetworksAddPeeringRequest{NetworkPeering: expandNetworkPeering(d)}
addOp, err := NewClient(config, userAgent).Networks.AddPeering(project, network, request).Do()
if err != nil {
    return fmt.Errorf("Error adding network peering: %s", err)
}
err = ComputeOperationWaitTime(config, addOp, project, "Adding Network Peering", userAgent, d.Timeout(schema.TimeoutCreate))

// AFTER
body := map[string]interface{}{
    "networkPeering": expandNetworkPeering(d),
}
url, err := tpgresource.ReplaceVars(d, config, "{{ComputeBasePath}}projects/{{project}}/global/networks/{{name}}/addPeering")
if err != nil {
    return err
}
res, err := transport_tpg.SendRequest(transport_tpg.SendRequestOptions{
    Config:    config,
    Method:    "POST",
    Project:   project,
    RawURL:    url,
    UserAgent: userAgent,
    Body:      body,
})
if err != nil {
    return fmt.Errorf("Error adding network peering: %s", err)
}
err = ComputeOperationWaitTime(config, res, project, "Adding Network Peering", userAgent, d.Timeout(schema.TimeoutCreate))
```

Note: `ComputeOperationWaitTime` now receives `res` (the `map[string]interface{}` returned by SendRequest) directly — the API returns an operation object for mutating calls.

### GET (read)

```go
// BEFORE
network, err := NewClient(config, userAgent).Networks.Get(project, networkName).Do()
if err != nil {
    return transport_tpg.HandleNotFoundError(err, d, fmt.Sprintf("Network %q", networkName))
}

// AFTER
url, err := tpgresource.ReplaceVars(d, config, "{{ComputeBasePath}}projects/{{project}}/global/networks/{{name}}")
if err != nil {
    return err
}
res, err := transport_tpg.SendRequest(transport_tpg.SendRequestOptions{
    Config:    config,
    Method:    "GET",
    Project:   project,
    RawURL:    url,
    UserAgent: userAgent,
})
if err != nil {
    return transport_tpg.HandleNotFoundError(err, d, fmt.Sprintf("Network %q", d.Get("name").(string)))
}
// res is now map[string]interface{} — use it like: network := res
```

### DELETE

```go
// BEFORE
op, err := NewClient(config, userAgent).TargetPools.Delete(project, region, name).Do()

// AFTER
url, err := tpgresource.ReplaceVars(d, config, "{{ComputeBasePath}}projects/{{project}}/regions/{{region}}/targetPools/{{name}}")
if err != nil {
    return err
}
res, err := transport_tpg.SendRequest(transport_tpg.SendRequestOptions{
    Config:    config,
    Method:    "DELETE",
    Project:   project,
    RawURL:    url,
    UserAgent: userAgent,
})
```

---

## Pattern 4 — Reading response fields

The API response is `map[string]interface{}`. Use type assertions to read values:

```go
// String field
selfLink, _ := res["selfLink"].(string)

// Bool field
autoCreate, _ := res["autoCreateSubnetworks"].(bool)

// Nested object
routingConfig, _ := res["routingConfig"].(map[string]interface{})
mode, _ := routingConfig["routingMode"].(string)

// List of objects
peerings, _ := res["peerings"].([]interface{})
for _, p := range peerings {
    peering, _ := p.(map[string]interface{})
    name, _ := peering["name"].(string)
}

// Setting on ResourceData (same as before — just pass the map value)
if err := d.Set("state", res["state"]); err != nil {
    return fmt.Errorf("Error setting state: %s", err)
}
if regionStr, ok := res["region"].(string); ok {
    d.Set("region", tpgresource.GetResourceNameFromSelfLink(regionStr))
}
```

---

## Pattern 5 — URL construction

Use `tpgresource.ReplaceVars()` when the URL fields come from ResourceData (project, region, zone, name).
Use `fmt.Sprintf()` + `transport_tpg.BaseUrl()` when you have the values directly (e.g., in helpers or tests).

```go
// With ResourceData — preferred for CRUD functions
url, err := tpgresource.ReplaceVars(d, config, "{{ComputeBasePath}}projects/{{project}}/global/networks/{{name}}")

// With explicit values — use when ResourceData isn't available
url := fmt.Sprintf("%sprojects/%s/global/networks/%s",
    transport_tpg.BaseUrl(Product, config), project, networkName)
```

### .tmpl file URL syntax

In `.go.tmpl` files the template engine will interpret `{{project}}` itself, so template
variables in string literals must be double-escaped:

```go
// In a .go.tmpl file — note the doubled braces around variable names
url, err := tpgresource.ReplaceVars(d, config, "{{"{{"}}ComputeBasePath{{"}}"}}projects/{{"{{"}}project{{"}}"}}/global/networks/{{"{{"}}name{{"}}"}}")
```

This looks odd but is correct — the outer `{{"{{"}}...{{"}}"}}` is how the template engine outputs
a literal `{{...}}` in the generated Go source.

---

## Pattern 6 — Error handling

```go
// 404 → resource gone (in Read)
return transport_tpg.HandleNotFoundError(err, d, fmt.Sprintf("Resource %q", d.Get("name").(string)))

// 404 with a specific message check (before the general handler)
if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == 404 && strings.Contains(gerr.Message, "httpHealthChecks") {
    return fmt.Errorf("Health check %s is not a valid HTTP health check", ...)
}
return fmt.Errorf("Error creating Resource: %s", err)

// 404 in Delete that should be silently ignored (resource already gone)
if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == 404 {
    log.Printf("[WARN] Resource already removed")
} else {
    return fmt.Errorf("Error removing: %s", err)
}
```

Keep `"google.golang.org/api/googleapi"` if you use `*googleapi.Error` assertions.

---

## Pattern 7 — Test file migration

In `_test.go.tmpl` files, replace Apiary client calls with `SendRequest`:

```go
// BEFORE
import tpgcompute "github.com/hashicorp/terraform-provider-google/google/services/compute"
...
found, err := tpgcompute.NewClient(config, config.UserAgent).Networks.Get(config.Project, name).Do()

// AFTER
import (
    tpgcompute "github.com/hashicorp/terraform-provider-google/google/services/compute"
    transport_tpg "github.com/hashicorp/terraform-provider-google/google/transport"
)
...
url := fmt.Sprintf("%sprojects/%s/global/networks/%s",
    transport_tpg.BaseUrl(tpgcompute.Product, config), config.Project, name)
found, err := transport_tpg.SendRequest(transport_tpg.SendRequestOptions{
    Config:    config,
    Method:    "GET",
    Project:   config.Project,
    RawURL:    url,
    UserAgent: config.UserAgent,
})
```

Access response fields the same way as in resource code (`found["name"].(string)`, etc.).

---

## HTTP method lookup

| Apiary call pattern              | REST method | URL pattern                     |
|----------------------------------|-------------|----------------------------------|
| `.Insert(proj, [region], body)`  | POST        | `.../resources` (collection)    |
| `.Get(proj, [region], name)`     | GET         | `.../resources/{name}`          |
| `.Delete(proj, [region], name)`  | DELETE      | `.../resources/{name}`          |
| `.Patch(proj, [region], name, b)`| PATCH       | `.../resources/{name}`          |
| `.Update(proj, [region], name,b)`| PUT         | `.../resources/{name}`          |
| `.AddPeering / .AddInstance`     | POST        | `.../resources/{name}/addX`     |
| `.RemovePeering / .RemoveInstance`| POST       | `.../resources/{name}/removeX`  |
| `.SetBackup / .SetSecurityPolicy`| POST        | `.../resources/{name}/setX`     |

Find the canonical REST URL from the [GCP Compute REST reference](https://cloud.google.com/compute/docs/reference/rest/v1/)
or look at the resource's `base_url` in `mmv1/products/compute/{Resource}.yaml`.

---

## Imports summary

**Remove:**
```go
"google.golang.org/api/compute/v1"
// or
compute "google.golang.org/api/compute/v0.beta"
```

**Keep/add:**
```go
transport_tpg "github.com/hashicorp/terraform-provider-google/google/transport"
"github.com/hashicorp/terraform-provider-google/google/tpgresource"
"google.golang.org/api/googleapi"  // only if using *googleapi.Error assertions
```