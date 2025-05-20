import React from "react";

function Navigation() {
    return (
        <div className="navigation">
            <ul>
                <li><a href="/">Home</a></li>
                <li><a href="/coalitions">Coalitions</a></li>
                <li><a href="/clients">Clients</a></li>
                <li><a href="/settings">Settings</a></li>
            </ul>
        </div>
    )
}

export default Navigation;