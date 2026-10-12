# TD2 - Google Compute Engine: answers to the questions

Team: Thomas, Youssef, Sabri
Project ID: cloud-opti-93260 - Zone: europe-west1-b - VM: cloudopt-vm1 (e2-standard-2)

## Part 0 - Compute Engine resource model

**1. What is the difference between a project, a region, and a zone?**
A project is an administrative container. It groups resources, permissions (IAM), quotas, enabled
APIs and billing. A region is a geographic area where Google runs data centers, for example
europe-west1 in Belgium. A zone is an isolated location inside a region, for example
europe-west1-b. A region contains several zones, so an application can survive the failure of one
zone.

**2. Is a VM instance a regional or zonal resource?**
Zonal. The VM is created in one zone (europe-west1-b for us) and stays there. To move it to another
zone you have to create a new VM there.

**3. What is the difference between a machine type and an operating-system image?**
The machine type defines the hardware resources given to the VM: number of vCPUs and amount of
memory (e2-standard-2 = 2 vCPUs, 8 GB). The image defines the software installed on the boot disk:
the operating system (Ubuntu 24.04 LTS for us). You can combine the same image with many machine
types.

**4. What is the difference between an image and a boot disk?**
The image is a read-only template. The boot disk is a persistent disk created from that image when
the VM is created. It belongs to our VM, we can write to it, and it keeps our files and installed
packages. Many boot disks can be created from the same image.

**5. Why might a machine type be available in one zone but temporarily unavailable in another?**
Each zone has its own physical servers. Not every CPU generation or series is installed in every
zone, and the capacity of a zone is limited. If many customers use a machine type in one zone at the
same time, there may be no free capacity left there for a while (error
ZONE_RESOURCE_POOL_EXHAUSTED), while another zone still has some.

## Part 1 - Google Cloud CLI

**1. Which Google account is currently active in gcloud?**
[fill in: the account marked with * in `gcloud auth list`]

**2. What is the Project ID selected by your CLI?**
cloud-opti-93260 (checked with `gcloud config list`).

**3. Why is selecting the correct project important before creating a VM?**
All resources, costs, quotas and permissions are attached to the project. With the wrong project
the VM would be billed to the wrong account, could fail because of missing permissions or quota,
and would not be visible to the rest of the team.

**4. What is the role of the Compute Engine API?**
It is the service that receives the requests to create, inspect, start, stop and delete VMs, disks,
networks and so on. gcloud and the web console both call this API. As long as it is not enabled in
the project, no Compute Engine resource can be created.

## Part 3 - Machine types

**1. How many vCPUs and how much RAM does your selected machine type provide?**
e2-standard-2 gives 2 vCPUs and 8 GB of memory. Inside the VM, `nproc` shows 2 and `free -h` shows
7.7 GiB, because part of the memory is reserved by the kernel. It is not a shared-core type.

**2. What does the term vCPU mean in the context of a VM?**
A vCPU is a virtual processor seen by the guest system. On Compute Engine one vCPU is one hardware
thread of a physical core. Our 2 vCPUs are the 2 hyperthreads of one physical core (lscpu shows
1 core and 2 threads per core). So 2 vCPUs is not the same as 2 full physical cores.

**3. Why can two machine types with the same number of vCPUs still provide different performance?**
Because the vCPU count says nothing about the CPU itself: generation, clock speed, cache size,
memory bandwidth, and whether the vCPU is a full core or a hyperthread. Some types also share
their CPU with other customers. In our tests, the Scaleway VM and the GCP VM both have 2 vCPUs, but
the GCP VM (Intel Broadwell at 2.2 GHz) was 1.7 times slower on memory_task and several times slower
on cpu_task than the Scaleway VM (AMD EPYC Zen 3).

**4. Why is it useful to record the exact machine type rather than writing only "Google Cloud VM"?**
"Google Cloud VM" covers machines from 0.25 to hundreds of vCPUs with very different speed and
price. Without the exact type and zone, nobody can rebuild the same machine, compare results, or
estimate the cost. Our scheduler later in the project will also need these characteristics to
assign tasks to machines.

## Part 5 - First VM

**1. What is the current status of the VM?**
RUNNING after creation. It is now deleted.

**2. Which machine type is attached to the VM?**
e2-standard-2.

**3. What is the name and size of its boot disk?**
cloudopt-vm1 (same name as the VM, by default), 20 GB, type pd-balanced, with autoDelete set to
true.

**4. Which network and subnet are used?**
The `default` VPC network and its `default` subnet in europe-west1.

**5. Does the VM have an external IP address?**
Yes, an ephemeral external IPv4 address assigned at creation (the natIP field).

## Part 6 - Networking

**1. What is your VM's internal IP address?**
[fill in: INTERNAL_IP column of `gcloud compute instances list`]

**2. Does it have an external IP address?**
Yes, an ephemeral one: [fill in: EXTERNAL_IP]. It is not reserved, so it can change after a stop and
start.

**3. Why might a production VM intentionally have no external IP?**
To reduce the attack surface: a machine with no public address cannot be reached directly from the
internet. It can still talk to other VMs through the internal network, go out through Cloud NAT,
and admins can connect through IAP or a bastion host. It also avoids paying for external IPs.

**4. Why is an external IP alone insufficient to guarantee that an SSH connection will work?**
The IP only says where the machine is. The traffic must also be allowed by the VPC firewall rules
(here `default-allow-ssh` on TCP port 22) and by any organization policy. On top of that the VM
must be running, the SSH service must be up, and our account must have a valid key or OS Login
permission.

## Part 12 - Analysis (local vs cloud)

The local machine of TD1 is a Scaleway PLAY2-NANO VM (AMD EPYC 7543, 2 vCPUs, 4 GB). The common
configuration is memory_task --size 512, run 3 times on each machine.

| Metric | Local (Scaleway) | Cloud (GCP e2-standard-2) |
|---|---|---|
| Mean runtime | 0.517 s | 0.897 s |
| Min - max | 0.51 - 0.52 s | 0.83 - 1.01 s |
| Spread | 1.9 % | 20.1 % |
| Max RSS | 527 564 kB | 526 972 kB |

**1. Is the cloud VM faster than your local machine for every workload?**
No. It was slower on every workload we could compare. memory_task at 512 MB takes 0.897 s on the
VM against 0.517 s locally, so 1.73 times longer. matrix_task reaches about 1.4 GFLOP/s on the VM
against 1.9 GFLOP/s locally. cpu_task is the worst case: 0.14 s on the VM for size 500 000 against
about 0.02 s locally. The cpu_task and matrix_task figures are only indicative because the sizes
were not the same on both machines.

**2. If not, why is "cloud" not itself a performance specification?**
"Cloud" only says where the machine runs and how we rent it. The speed depends on the machine type,
the CPU generation behind it, the zone, and the load from other customers on the same host. Here a
2 vCPU cloud VM with 8 GB is slower than a small 2 vCPU Scaleway VM with 4 GB, because the GCP host
uses an older Intel Broadwell CPU (2.2 GHz) while the Scaleway host uses an AMD Zen 3 CPU. cpu_task
is dominated by integer divisions (n % d), which are much slower on Broadwell.

**3. Are memory measurements similar between local and cloud execution for the same workload?**
Yes. For memory_task 512 the maximum RSS is 527 564 kB locally and 526 972 kB on the VM, a
difference of 0.1 %. The memory footprint depends on the program and its input, not on the machine.
Only the Go runtime baseline differs a little (about 1.9 MB on the VM against 2.4 MB locally for
cpu_task).

**4. Are repeated runtimes identical on the VM?**
No. memory_task varies a lot: 0.83 to 1.01 s at 512 MB (20 % spread) and 0.40 to 0.50 s at 256 MB,
where the first run was the slowest. Locally the spread was under 2 %. cpu_task and matrix_task
look perfectly stable on the VM, but only because their runs last 0.01 to 0.37 s and /usr/bin/time
measures in steps of 10 ms. Longer runs would show some variation too.

**5. Which VM characteristics are necessary to make your comparison reproducible?**
Provider, project, region and zone; exact machine type; number of vCPUs and threads per core;
CPU model seen by lscpu (the same E2 type can run on Intel or AMD hosts); memory size; image family
and exact image, kernel version; disk type and size; Go version; and the workload sizes and number
of runs. We also keep the raw runs in the CSV, not only the averages.

## What we would do differently

- Run the cloud VM with exactly the TD1 sizes (cpu_task 8M/16M/32M, memory_task 512/1024/2048,
  matrix_task 1000/1500/2000). This time only memory_task 512 was common to both machines.
- Keep every run longer than 0.5 s so the 10 ms timer resolution stays below 2 %.
- Use 5 runs instead of 3 on cloud VMs, since their variability is higher.
