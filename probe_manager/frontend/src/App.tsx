import {useState} from 'react';
import logo from './assets/images/logo-universal.png';
import './App.css';
import {Greet} from "../wailsjs/go/main/App";

function App() {
    const [resultText, setResultText] = useState("Please enter your name below 👇");
    const [serverStatus, setServerStatus] = useState("Local Server: Unknown");
    const [name, setName] = useState('');
    const updateName = (e: any) => setName(e.target.value);
    const updateResultText = (result: string) => setResultText(result);

    function greet() {
        Greet(name).then(updateResultText);
    }

    async function pingServer() {
        try {
            const response = await fetch("http://127.0.0.1:15030/api/ping");
            const data = await response.json();
            setServerStatus(`Probe Panel: ${data.message} from ${data.service}`);
        } catch (e: any) {
            setServerStatus(`Probe Panel Error: ${e.message}`);
        }
    }

    return (
        <div id="App">
            <img src={logo} id="logo" alt="logo"/>
            <div id="result" className="result">{resultText}</div>
            <div id="input" className="input-box">
                <input id="name" className="input" onChange={updateName} autoComplete="off" name="input" type="text"/>
                <button className="btn" onClick={greet}>Greet</button>
            </div>
            <div style={{marginTop: "20px"}}>
                <div style={{color: "white", marginBottom: "10px"}}>{serverStatus}</div>
                <button className="btn" onClick={pingServer}>Ping Probe Panel</button>
            </div>
        </div>
    )
}

export default App
