const form = document.getElementById("signupForm");
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
    showError("");

    const username = form.querySelector("[name='username']");
    const email = form.querySelector("[name='email']");
    const password = form.querySelector("[name='password']");
    const confirm = form.querySelector("[name='confirm_password']");

    if (
        !username.value.trim() ||
        !email.value.trim() ||
        !password.value.trim() ||
        !confirm.value.trim()
    ) {
        showError("Please fill out all fields.");
        return;
    }

    if (email.validity.typeMismatch) {
        showError("Please enter a valid email address.");
        email.focus();
        return;
    }

    if (password.value !== confirm.value) {
        showError("Passwords do not match.");
        confirm.focus();
        return;
    }

    const formData = new FormData(form);

    try {
        const resp = await fetch("/signup", {
            method: "POST",
            body: formData,
            credentials: "include",
        });

        let data;
        try {
            data = await resp.json();
        } catch {
            showError("Unexpected server response.");
            return;
        }

        if (!resp.ok || !data.ok) {
            showError(data.error || "Could not create account.");
            return;
        }

        window.location.href = data.redirect || "/login";

    } catch (err) {
        console.error("Signup error:", err);
        showError("Could not contact the server. Please try again.");
    }
});