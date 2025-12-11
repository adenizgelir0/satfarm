const form = document.getElementById("loginForm");
const errorBox = document.getElementById("errorBox");

function showError(msg) {
    if (!msg) {
        errorBox.classList.add("hidden");
        errorBox.textContent = "";
    } else {
        errorBox.classList.remove("hidden");
        errorBox.textContent = msg;
    }
}

form.addEventListener("submit", async (e) => {
    e.preventDefault();

    const username = form.querySelector("[name='username']");
    const password = form.querySelector("[name='password']");

    if (!username.value.trim() || !password.value.trim()) {
        showError("Please fill out all fields.");
        return;
    }

    const resp = await fetch("/login", {
        method: "POST",
        body: new FormData(form),
        credentials: "include",
    });

    const data = await resp.json();

    if (!resp.ok || !data.ok) {
        showError(data.error || "Login failed.");
    } else {
        window.location.href = data.redirect || "/dashboard";
    }
});