# Cloud Optimization – Projet semestriel (ESILV A5 CCC)

Équipe : Thomas , Youssef

## Environnement TD1
- VM Linuw (2 vCPU partagés, 4 Go RAM)
- Ubuntu 24.04 LTS

## Installation
    apt update
    apt install -y git make build-essential time golang-go htop sysstat

## Compilation
    go build -o bin/cpu_task workloads/cpu_task.go
    go build -o bin/memory_task workloads/memory_task.go
    go build -o bin/matrix_task workloads/matrix_task.go
    go build -o bin/sort_task workloads/sort_task.go

## Exécution
    ./bin/cpu_task --size 500000
    ./bin/memory_task --size 256
    ./bin/matrix_task --size 400
    ./bin/sort_task --size 1000000

## Structure
- workloads/ : sources Go fournies
- bin/       : exécutables compilés
- data/      : caractéristiques machine et mesures brutes
- results/   : analyses et graphiques
- scripts/   : scripts de benchmark et d'automatisation
