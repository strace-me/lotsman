package config

import "testing"

// Two instances, no qnum named: the queues must be derived disjointly. An
// operator naming several instances almost cannot pick disjoint numbers by hand,
// and the collision that produces is silent — two engines on one nfqueue steal
// each other's packets while every status still reads healthy.
func TestZapretInstancesDeriveDisjointQueues(t *testing.T) {
	doc, err := ParseDocument([]byte(`
zapret:
  instances:
    - name: nfqws_0
      capture: { tcp: [443] }
    - name: nfqws_udp
      capture: { udp: [50000-50100] }
`))
	if err != nil {
		t.Fatal(err)
	}
	instances, err := buildZapret(doc.Zapret)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("instances = %#v", instances)
	}
	if instances[0].QNum == instances[1].QNum {
		t.Fatalf("derived queues collide: %d and %d", instances[0].QNum, instances[1].QNum)
	}
	// The first instance keeps the historical base so an existing single-instance
	// config lands on exactly the queue it did before.
	if instances[0].QNum != 200 {
		t.Errorf("first instance queue = %d, want the historical base 200", instances[0].QNum)
	}
}

func TestZapretInstanceHonoursAnExplicitQueue(t *testing.T) {
	doc, err := ParseDocument([]byte(`
zapret:
  instances:
    - name: nfqws_0
      qnum: 100
      capture: { tcp: [443] }
`))
	if err != nil {
		t.Fatal(err)
	}
	instances, err := buildZapret(doc.Zapret)
	if err != nil {
		t.Fatal(err)
	}
	if instances[0].QNum != 100 {
		t.Errorf("explicit qnum was overwritten: %d", instances[0].QNum)
	}
}

func TestZapretInstancesStillRefuseOverlappingCaptures(t *testing.T) {
	// Deriving the queues must not paper over the port constraint: a packet still
	// reaches exactly one queue, so two instances cannot both claim tcp/443.
	doc, err := ParseDocument([]byte(`
zapret:
  instances:
    - name: nfqws_0
      capture: { tcp: [443] }
    - name: nfqws_1
      capture: { tcp: [443] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildZapret(doc.Zapret); err == nil {
		t.Fatal("two instances claiming tcp/443 were accepted")
	}
}
