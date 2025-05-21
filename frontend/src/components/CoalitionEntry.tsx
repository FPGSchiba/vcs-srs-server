import * as React from 'react';
import {state} from "../../wailsjs/go/models";

function CoalitionEntry(props: Readonly<{ coalition: state.Coalition }>) {
    const { coalition } = props;

    return (
        <div>
            <h2>{coalition.Name}</h2>
            <p>This is a placeholder for the Coalition Entry component.</p>
        </div>
    );
}

export default CoalitionEntry;