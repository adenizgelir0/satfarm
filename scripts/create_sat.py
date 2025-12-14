import random
random.seed(10)

n_vars = 263
n_clauses = int(n_vars * 4.26)

with open(f"hard{n_vars}.cnf", "w") as f:
    f.write("c Random 3-SAT instance\n")
    f.write(f"c {n_vars} variables, {n_clauses} clauses\n")
    f.write(f"c ratio = {n_clauses/n_vars:.2f}\n")
    f.write(f"p cnf {n_vars} {n_clauses}\n")

    for _ in range(n_clauses):
        vars = random.sample(range(1, n_vars + 1), 3)
        clause = [v if random.random() > 0.5 else -v for v in vars]
        f.write(" ".join(map(str, clause)) + " 0\n")

