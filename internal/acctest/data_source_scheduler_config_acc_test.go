package acctest

// Acceptance tests for the anyscale_scheduler_config data source.
//
// These run against the scheduler mock server rather than a real organization:
// the behaviors under test are what the data source does with a given wire
// response (a populated document, an omitted section, a 404, a 403), and only a
// mock can put the API into all four states on demand. The real-API read is
// covered by the resource's own suite.

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const schedulerConfigDataSourceName = "data.anyscale_scheduler_config.test"

const schedulerConfigDataSourceConfig = `
data "anyscale_scheduler_config" "test" {}
`

// A populated document read through the data source: every section present,
// nested values intact, and the version/created_at/creator_id metadata carried
// through alongside it.
func TestAccSchedulerConfigDataSourceReadsActiveConfig(t *testing.T) {
	const readConfig = `{
	  "resource_flavors": [
	    {
	      "name": "cpu-standard",
	      "selector": [{"key": "node.kubernetes.io/instance-type", "operator": "in", "values": ["m5.large"]}],
	      "advanced_instance_config": {"IamInstanceProfile": {"Name": "sched"}}
	    }
	  ],
	  "resource_queues": [
	    {
	      "name": "default",
	      "cohort_name": "shared",
	      "preemption": {"reclaim_within_cohort": "lower_priority"},
	      "resource_groups": [
	        {
	          "covered_resources": ["cpu"],
	          "flavors": [
	            {"name": "cpu-standard", "resources": [{"name": "cpu", "nominal_quota": 0, "borrowing_limit": 8}]}
	          ]
	        }
	      ]
	    }
	  ],
	  "scheduling_rules": [
	    {
	      "resource_queue": "default",
	      "selector": [{"key": "team", "operator": "exists"}],
	      "priority_policy": {"default": 5, "min": 0, "max": 10, "on_violation": "reject"}
	    }
	  ],
	  "recycle_policy": {"rotation_interval": "24h", "max_workloads": 100}
	}`

	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     readConfig,
		InitialVersion: 7,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerConfigDataSourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "version", "7"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "created_at", "2026-09-09T00:00:00Z"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "creator_id", "usr_mock"),

					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.#", "1"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.0.name", "cpu-standard"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.0.selector.0.key", "node.kubernetes.io/instance-type"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.0.selector.0.operator", "in"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.0.selector.0.values.0", "m5.large"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.0.advanced_instance_config", `{"IamInstanceProfile":{"Name":"sched"}}`),

					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.name", "default"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.cohort_name", "shared"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.preemption.reclaim_within_cohort", "lower_priority"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.resource_groups.0.covered_resources.0", "cpu"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.resource_groups.0.flavors.0.name", "cpu-standard"),
					// An explicit 0 quota must survive as 0, not collapse to
					// null: on this API the two mean "blocked" and "unlimited".
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.resource_groups.0.flavors.0.resources.0.nominal_quota", "0"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.resource_groups.0.flavors.0.resources.0.borrowing_limit", "8"),
					// A quota field the document omits stays null rather than
					// reading back as 0 - the same distinction from the other side.
					resource.TestCheckNoResourceAttr(schedulerConfigDataSourceName, "resource_queues.0.resource_groups.0.flavors.0.resources.0.lending_limit"),

					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.0.resource_queue", "default"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.0.selector.0.operator", "exists"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.0.priority_policy.default", "5"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.0.priority_policy.on_violation", "reject"),

					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "recycle_policy.rotation_interval", "24h"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "recycle_policy.max_workloads", "100"),
					resource.TestCheckNoResourceAttr(schedulerConfigDataSourceName, "recycle_policy.max_idle_duration"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					// `exists` takes no values, and null is not the same as an
					// empty list here. Asserted as a state check because the
					// TestCheckNoResourceAttr form cannot tell those apart on a
					// list - see the omitted-sections test for why.
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName,
						tfjsonpath.New("scheduling_rules").AtSliceIndex(0).AtMapKey("selector").AtSliceIndex(0).AtMapKey("values"),
						knownvalue.Null()),
				},
			},
		},
	})
}

// A section the config does not set reads back as null, not as an empty list.
//
// The assertions are statecheck.ExpectKnownValue against knownvalue.Null(),
// because neither TestCheckResourceAttr(..., ".#", "0") nor
// TestCheckNoResourceAttr can tell null from empty here. The count form
// obviously passes on an empty list - that is what an empty list reports. The
// no-attr form looks like it would catch one, but it carves out count keys
// whose value is "0" and treats them as absent, so it passes on an empty list
// too. Both were verified against a build that materialized an empty slice:
// both stayed green, this one goes red.
func TestAccSchedulerConfigDataSourceOmittedSectionsReadAsNull(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     `{"resource_flavors":[{"name":"cpu-standard"}]}`,
		InitialVersion: 3,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerConfigDataSourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					// Positive control: the one section the document does set is
					// present, so a wholly empty read cannot pass this test.
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.#", "1"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("resource_queues"), knownvalue.Null()),
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("scheduling_rules"), knownvalue.Null()),
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("recycle_policy"), knownvalue.Null()),
					// The flavor's own unset sub-fields, same rule one level down.
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("resource_flavors").AtSliceIndex(0).AtMapKey("selector"), knownvalue.Null()),
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("resource_flavors").AtSliceIndex(0).AtMapKey("advanced_instance_config"), knownvalue.Null()),
				},
			},
		},
	})
}

// An active config that sets nothing reads back with all four sections null.
//
// The companion to the test above, which cannot cover resource_flavors: that
// section is its positive control there, so it is necessarily non-null. Here
// the served document is empty and every section is asserted null, with the
// metadata fields standing in as the positive control - they prove a read
// happened at all, so an entirely blank state cannot pass.
//
// recycle_policy is the section worth the extra step, for two reasons.
//
// Mechanically, it is a single nested object, so its count key is `.%` rather
// than `.#` - and TestCheckNoResourceAttr treats both suffixes identically, so
// the placebo described above applies unchanged in a different spelling. That
// is the half this test proves: materialize an empty policy and it goes red.
//
// Substantively, the distinction is observable on the wire rather than mere
// state hygiene. The three list sections are dropped by `omitempty` on a slice,
// which treats a len-0 slice as absent; `omitempty` on a struct pointer tests
// only nilness, so an unconditionally allocated empty struct really does emit
// `recycle_policy: {}`, and the server preserves that empty object rather than
// collapsing it. A read that turned an unset policy into `{}` would therefore
// misreport live server state.
//
// The wire behavior above was established during design against the real API;
// it is recorded here rather than cited because nothing in this test exercises
// it. What is verified below is the provider half only - that an absent key
// maps to null. Do not read a green run as confirmation of the server half.
func TestAccSchedulerConfigDataSourceEmptyConfigReadsAllSectionsNull(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     `{}`,
		InitialVersion: 5,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerConfigDataSourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					// Positive control: the read really happened.
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "version", "5"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "created_at", "2026-09-09T00:00:00Z"),
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("resource_flavors"), knownvalue.Null()),
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("resource_queues"), knownvalue.Null()),
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("scheduling_rules"), knownvalue.Null()),
					statecheck.ExpectKnownValue(schedulerConfigDataSourceName, tfjsonpath.New("recycle_policy"), knownvalue.Null()),
				},
			},
		},
	})
}

// No active config is an error, not an empty document.
//
// The mock answers the real 404 body the API sends. That matters for more than
// realism: the data source must not route this read through DoRequestAndParse
// with 404 in its accepted-status list, which would swallow the status and hand
// back a zero-valued response - an organization with no scheduler config would
// then read as a config with every section unset, and every downstream
// reference would silently see nothing rather than fail.
func TestAccSchedulerConfigDataSourceErrorsWhenNoActiveConfig(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		GetStatus: 404,
		GetBody:   `{"error":{"detail":"No active scheduler config found."}}`,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerConfigDataSourceConfig,
				// The diagnostic must name the resource as the way to create
				// one; "not found" alone leaves the practitioner nowhere.
				ExpectError: regexp.MustCompile(`(?s)No Active Scheduler Config.*anyscale_scheduler_config`),
			},
		},
	})
}

// A disabled scheduler is an error carrying the translated message, not an
// empty document.
//
// The resource's Read fails open on this same 403, and the difference is
// deliberate: that fail-open is licensed by prior state the provider can leave
// untouched, not by the status code. A data source has no prior state, so
// falling open would publish an empty config to everything downstream.
func TestAccSchedulerConfigDataSourceErrorsWhenSchedulerDisabled(t *testing.T) {
	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		GetStatus: 403,
		GetBody:   `{"error":{"detail":"Global Resource Scheduler is not enabled for this organization."}}`,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccProviderBlock(server.URL) + schedulerConfigDataSourceConfig,
				ExpectError: regexp.MustCompile(`(?s)Anyscale Scheduler Not Enabled.*not enabled for this organization`),
			},
		},
	})
}

// List order is read back exactly as the API served it.
//
// Order is significant to the scheduler - flavors are tried in order, and
// scheduling rules are first-match-wins - so a read that sorted or otherwise
// normalized would be a real defect and an invisible one, since every element
// would still be present. The expectations are hand-written against a
// hand-written read-back document rather than derived from it, which is what
// makes them capable of disagreeing with the implementation.
func TestAccSchedulerConfigDataSourcePreservesOrder(t *testing.T) {
	const readConfig = `{
	  "resource_flavors": [{"name": "gpu-h100"}, {"name": "cpu-standard"}, {"name": "gpu-a10"}],
	  "scheduling_rules": [
	    {"resource_queue": "priority"},
	    {"resource_queue": "batch"},
	    {"resource_queue": "default"}
	  ]
	}`

	server, _ := newSchedulerConfigServer(t, schedulerConfigServerOpts{
		ReadConfig:     readConfig,
		InitialVersion: 1,
	})
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccProviderBlock(server.URL) + schedulerConfigDataSourceConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.#", "3"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.0.name", "gpu-h100"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.1.name", "cpu-standard"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "resource_flavors.2.name", "gpu-a10"),

					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.#", "3"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.0.resource_queue", "priority"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.1.resource_queue", "batch"),
					resource.TestCheckResourceAttr(schedulerConfigDataSourceName, "scheduling_rules.2.resource_queue", "default"),
				),
			},
		},
	})
}
